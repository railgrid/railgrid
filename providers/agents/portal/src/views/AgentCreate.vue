<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref, watch } from 'vue'
import { ArrowLeft, Bot, Check, Clock } from 'lucide-vue-next'
import FormSelect, { type FormSelectOption } from '../portalkit/FormSelect.vue'
import CreateGuidance from '../portalkit/CreateGuidance.vue'
import { portalHref } from '../portalkit/navigation'
import { toast } from '../portalkit/toast'
import { mutate } from '../mutate'
import type { ApiClient } from '../api'
import {
  instantiateTemplate,
  previewPrompt,
  slotsOfKind,
  templateBackend,
  templateFamilies,
  type AgentTemplate,
  type TemplateSlotKind,
  type TemplateWiringSlot,
} from '../agent-template'
import type { AppStore } from '../store'
import { emptyAgentCreateDraft, type AgentCreateDraft, type AgentCredentialFamily } from '../agent-create-draft'
import type { CreateSuccessDetail, Route } from '../router'
import {
  AGENT_BACKEND_HARNESS,
  AGENT_BACKEND_MODEL,
  HARNESS_EDGE_KINDS,
  WORKSPACE_MODES,
  edgeKey,
  harnessLabel,
  splitEdgeKey,
  type Agent,
  type AgentBackendType,
  type AgentCreate as AgentCreateBody,
  type HarnessWorkspace,
} from '../types'
import { useStoreRevision } from '../vue/runtime'

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

// The backend choice, with the same two labels and blurbs the Config pane
// shows. One decision described one way, or a person meets it twice and reads
// it as two.
const BACKENDS: { id: AgentBackendType; label: string; blurb: string }[] = [
  { id: AGENT_BACKEND_MODEL, label: 'Model', blurb: 'Uses a model connection and the tools configured for this agent.' },
  { id: AGENT_BACKEND_HARNESS, label: 'Coding harness', blurb: 'Runs on a Linux or macOS machine and uses its own tools. Agent tool grants do not apply.' },
]

const initialDraft = props.initialDraft || emptyAgentCreateDraft(props.initialBackendType, props.initialTemplate)
const name = ref(initialDraft.name)
// Templates: the catalog is a bundle asset (see api.agentTemplates). The chosen
// template drives the backend, the parameter fields and which wiring is asked
// for; '' is the blank form this view always was.
const templates = ref<AgentTemplate[]>([])
const templatesError = ref('')
const template = ref(initialDraft.template || props.initialTemplate || '')
const parameters = ref<Record<string, string>>({ ...initialDraft.parameters })
// Connection-slot wiring, by slot key. The other slot kinds reuse the fields
// below (model credential, channel, machine, harness identity, GitHub).
const slotWiring = ref<Record<string, string>>({ ...initialDraft.wiring })
// The template's instructions render read-only from the parameters until the
// user asks to edit them; then the textarea takes over as an override.
const customPrompt = ref(false)
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
const web = ref(initialDraft.web)
const fanOut = ref(initialDraft.fanOut)
const visualization = ref(initialDraft.visualization)
// Background runs (schedules, triggers) have no human watching, so a family
// stays interactive-only unless opted in here — same rule as the Config pane.
const webBackground = ref(initialDraft.webBackground)
const fanOutBackground = ref(initialDraft.fanOutBackground)
const visualizationBackground = ref(initialDraft.visualizationBackground)
watch(visualization, on => { if (!on) visualizationBackground.value = false })
watch(web, on => { if (!on) webBackground.value = false })
watch(fanOut, on => { if (!on) fanOutBackground.value = false })
const errors = reactive<Record<string, string>>({})
const busy = ref(false)
const nameInput = ref<HTMLInputElement | null>(null)

const agents = computed(() => { revision.value; return { ...props.store.agents } })
const credentials = computed(() => { revision.value; return { ...props.store.credentials } })
const isHarness = computed(() => backendType.value === AGENT_BACKEND_HARNESS)
// Two credential families, and the API rejects one where the other belongs: a
// chat endpoint cannot drive a coding harness, and a `claude setup-token` value
// is not a bearer any chat API would accept. So each picker offers only its own.
const chatCredentials = computed(() => { revision.value; return props.store.chatCredentials() })
const harnessCredentials = computed(() => { revision.value; return props.store.harnessCredentials() })
const edgeSlice = computed(() => { revision.value; return { ...props.store.edges } })
// A runner is a process on a machine, so a KubernetesCluster edge can never host
// one. It is filtered here as well as refused by the write path: an option that
// cannot work should not be offered, not merely rejected after being picked.
const hostEdges = computed(() => edgeSlice.value.data.filter(edge => HARNESS_EDGE_KINDS.includes(edge.kind)))
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
const selectedHarness = computed(() => harnessLabel(
  harnessCredentials.value.find(credential => credential.name === harnessCredential.value)?.provider,
))
const channels = computed(() => { revision.value; return props.store.channelConnections() })
const githubConnections = computed(() => { revision.value; return props.store.githubConnections() })
const githubConnectionOptions = computed(() => [
  { value: '', label: '— none —' },
  ...githubConnections.value.map(item => ({ value: item.metadata.name, label: item.spec.displayName || item.metadata.name })),
])
const selectedTemplate = computed(() => templates.value.find(item => item.metadata.name === template.value) || null)
const templateParameters = computed(() => selectedTemplate.value?.spec.parameters ?? [])
const connectionSlots = computed(() => selectedTemplate.value ? slotsOfKind(selectedTemplate.value, 'connection') : [])
function slotOf(kind: TemplateSlotKind): TemplateWiringSlot | undefined {
  return selectedTemplate.value ? slotsOfKind(selectedTemplate.value, kind)[0] : undefined
}
const channelSlot = computed(() => slotOf('channel'))
const githubSlot = computed(() => slotOf('githubConnection'))
const connections = computed(() => { revision.value; return props.store.connections.data })
function connectionOptions(type: string, required: boolean): FormSelectOption[] {
  return [
    { value: '', label: required ? '— pick a connection —' : '— none —' },
    ...connections.value.filter(item => item.spec.type === type).map(item => ({ value: item.metadata.name, label: item.spec.displayName || item.metadata.name })),
  ]
}
const SLOT_LABELS: Record<TemplateSlotKind, string> = {
  modelCredential: 'model credential', channel: 'channel', connection: 'connection',
  edge: 'machine', harnessCredential: 'harness identity', githubConnection: 'GitHub connection',
}
function slotLabel(slot: TemplateWiringSlot): string {
  return slot.slot === 'connection' ? `${slot.connectionType ?? ''} connection`.trim() : SLOT_LABELS[slot.slot]
}
function templateNeeds(item: AgentTemplate): string {
  return (item.spec.wiring ?? []).map(slotLabel).join(' · ')
}
const promptPreview = computed(() => selectedTemplate.value ? previewPrompt(selectedTemplate.value, parameters.value) : '')
watch(selectedTemplate, (current, previous) => {
  if (!current) { customPrompt.value = false; return }
  backendType.value = templateBackend(current)
  const suggested = current.spec.suggestedName || current.metadata.name
  const previousSuggested = previous ? previous.spec.suggestedName || previous.metadata.name : ''
  if (!name.value.trim() || (previousSuggested && name.value === previousSuggested)) name.value = suggested
  if (previous && previous.metadata.name !== current.metadata.name) {
    parameters.value = {}
    slotWiring.value = {}
    customPrompt.value = false
  }
}, { immediate: true })
async function loadTemplates(): Promise<void> {
  try {
    templates.value = await props.api.agentTemplates()
    templatesError.value = ''
  } catch (e) {
    templatesError.value = (e as Error).message
  }
}
function customizePrompt(): void {
  systemPrompt.value = promptPreview.value
  customPrompt.value = true
}
const credentialOptions = computed(() => chatCredentials.value.map(item => ({
  value: item.name,
  label: item.connectionRef ? `${item.connectionName || item.connectionRef} · ${item.displayName || item.model}` : `${item.name}${item.model ? ` (${item.model})` : ''}`,
})))
const channelOptions = computed(() => [
  { value: '', label: '— none —' },
  ...channels.value.map(item => ({
    value: item.metadata.name,
    label: `${item.spec.displayName || item.metadata.name} (${item.spec.type})`,
  })),
])
const capabilities = computed(() => isHarness.value ? 'The harness’s own tools' : selectedTemplate.value ? (templateFamilies(selectedTemplate.value).join(', ') || 'Core only') : [
  visualization.value ? `charts${visualizationBackground.value ? ' (+background)' : ''}` : '',
  web.value ? `web${webBackground.value ? ' (+background)' : ''}` : '',
  fanOut.value ? `fan-out${fanOutBackground.value ? ' (+background)' : ''}` : '',
].filter(Boolean).join(', ') || 'Core only')

// What the guidance panel promises, per backend. A harness-backed agent's
// prerequisite is a machine and an identity, not a model credential — telling
// somebody to add one would send them to fix a thing that is not missing.
const prerequisites = computed(() => isHarness.value
  ? [
    hostEdges.value.length
      ? 'A Linux or macOS machine is joined to this workspace.'
      : edgeSlice.value.error
        ? 'Retry loading machines before choosing a runner.'
        : edgeSlice.value.hasSnapshot
          ? 'Join a Linux or macOS machine before creating the agent.'
          : 'Checking for available machines…',
    harnessCredentials.value.length
      ? 'A Claude Code or Codex identity is available in this workspace.'
      : 'Add a Claude Code or Codex identity under Models before creating the agent.',
  ]
  : [
    credentialOptions.value.length
      ? 'A model credential is available in this workspace.'
      : 'Add a model credential before creating the agent.',
    'Optional channel connections can be added now or attached later from Config.',
  ])
const summaryValues = computed(() => [
  { label: 'Start from', value: selectedTemplate.value?.spec.displayName || 'Blank' },
  { label: 'Agent name', value: name.value.trim() || 'Not entered yet', technical: true },
  ...(isHarness.value
    ? [
      { label: 'Machine', value: splitEdgeKey(harnessEdge.value).name || 'Not selected', technical: true },
      { label: 'Harness', value: selectedHarness.value || 'Not selected', technical: true },
      { label: 'Identity', value: harnessCredential.value || 'Not selected', technical: true },
    ]
    : [{ label: 'Model', value: modelCredential.value || 'Not selected', technical: true }]),
  { label: 'Primary channel', value: channel.value || 'None', technical: true },
  { label: 'Capabilities', value: capabilities.value },
  ...(selectedTemplate.value?.spec.schedules?.length
    ? [{ label: 'Schedules', value: selectedTemplate.value.spec.schedules.map(item => `${name.value.trim() || '<name>'}-${item.name}`).join(', '), technical: true }]
    : []),
])
const guidanceDescription = computed(() => isHarness.value
  ? 'Choose the machine and harness identity that will run the agent’s turns.'
  : 'Choose the model connection the agent will use for its turns.')
const guidanceNextSteps = computed(() => [
  selectedTemplate.value
    ? 'Railgrid creates the agent (and any schedules the template declares) and opens its Config workspace.'
    : 'Railgrid creates the agent and opens its Config workspace.',
  'Start a conversation to make sure the agent responds as expected.',
  ...(isHarness.value
    ? ['Add schedules and triggers when you’re ready to automate it.']
    : ['Attach toolsets, schedules, and triggers when the core behavior is ready.']),
])
// Whether the form can be submitted at all, which is a different question per
// backend: the old condition was "a chat credential exists", which made the
// harness backend unreachable however it was configured.
const canSubmit = computed(() => isHarness.value
  ? hostEdges.value.length > 0 && harnessCredentials.value.length > 0
  : credentialOptions.value.length > 0)

onMounted(() => {
  void nextTick(() => nameInput.value?.focus())
  void loadTemplates()
})

function clearErrors(): void {
  for (const key of Object.keys(errors)) delete errors[key]
}

function addCredential(family: AgentCredentialFamily): void {
  if (busy.value) return
  emit('add-credential', {
    family,
    draft: {
      name: name.value,
      backendType: backendType.value,
      modelCredential: modelCredential.value,
      harnessEdge: harnessEdge.value,
      harnessCredential: harnessCredential.value,
      harnessModel: harnessModel.value,
      harnessReasoningEffort: harnessReasoningEffort.value,
      harnessWorkspace: harnessWorkspace.value,
      harnessGitHubConnection: harnessGitHubConnection.value,
      systemPrompt: systemPrompt.value,
      channel: channel.value,
      web: web.value,
      fanOut: fanOut.value,
      visualization: visualization.value,
      webBackground: webBackground.value,
      fanOutBackground: fanOutBackground.value,
      visualizationBackground: visualizationBackground.value,
      template: template.value,
      parameters: { ...parameters.value },
      wiring: { ...slotWiring.value },
    },
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
    if (!harnessEdge.value) errors.harnessEdge = 'Pick the machine this agent runs its turns on.'
    if (!harnessCredential.value) errors.harnessCredential = 'Pick the harness identity its turns run as.'
  } else if (!modelCredential.value) {
    errors.modelCredential = 'Pick the model this agent reasons with.'
  }
  const chosen = selectedTemplate.value
  if (chosen) {
    for (const param of templateParameters.value) {
      if (param.required && !(parameters.value[param.key] ?? '').trim() && !param.default) errors[`param-${param.key}`] = `${param.label} is required.`
    }
    if (channelSlot.value?.required && !channel.value) errors.channel = 'This template posts to a channel; pick one.'
    if (githubSlot.value?.required && !harnessGitHubConnection.value) errors.harnessGitHubConnection = 'This template needs a GitHub connection.'
    for (const slot of connectionSlots.value) {
      if (slot.required && !(slotWiring.value[slot.key] ?? '')) errors[`slot-${slot.key}`] = `Pick a ${slotLabel(slot)}.`
    }
  }
  if (Object.keys(errors).length) return
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
      ...(harnessReasoningEffort.value.trim() ? { reasoningEffort: harnessReasoningEffort.value.trim() } : {}),
      workspace: harnessWorkspace.value,
      ...(harnessGitHubConnection.value ? { githubConnectionRef: harnessGitHubConnection.value } : {}),
    }
  } else {
    body.modelCredential = modelCredential.value
  }
  const prompt = systemPrompt.value.trim()
  if (prompt) body.systemPrompt = prompt
  if (channel.value) body.channels = [{ name: 'primary', connectionRef: channel.value, primary: true }]
  // Tool grants only for a model backend: the API refuses spec.tools on a
  // harness-backed agent, so sending them would fail the create outright.
  if (!isHarness.value) {
    const families = ['core']
    if (visualization.value) families.push('visualization')
    if (web.value) families.push('web')
    if (fanOut.value) families.push('spawn')
    if (families.length > 1) body.interactiveFamilies = families
    const background = ['core']
    if (visualization.value && visualizationBackground.value) background.push('visualization')
    if (web.value && webBackground.value) background.push('web')
    if (fanOut.value && fanOutBackground.value) background.push('spawn')
    if (background.length > 1) body.backgroundFamilies = background
  }

  busy.value = true
  let result: Agent | undefined
  try {
    result = await mutate(props.store, {
      run: () => props.api.createAgent(body),
      success: `Agent “${normalizedName}” created.`,
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
    instance = instantiateTemplate(chosen, { name: normalizedName, parameters: parameters.value, wiring })
  } catch (e) {
    errors.template = (e as Error).message
    return
  }
  if (customPrompt.value && systemPrompt.value.trim()) instance.agent.spec.systemPrompt = systemPrompt.value.trim()
  const harness = instance.agent.spec.backend?.harness
  if (harness) {
    if (harnessModel.value.trim()) harness.model = harnessModel.value.trim()
    harness.workspace = harnessWorkspace.value
  }
  const { agent, schedules } = instance

  busy.value = true
  let result
  try {
    result = await mutate(props.store, {
      run: () => props.api.createAgentFromTemplate(agent, schedules),
      success: `Agent “${normalizedName}” created from ${chosen.spec.displayName}.`,
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
    <header class="k-create-header">
      <h1 class="k-create-title">Create agent</h1>
      <p class="k-create-description">Start from a template or a blank agent, choose where its turns run, and wire in what it needs from this workspace.</p>
    </header>

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

      <form class="agents-create-form agents-guided-form k-create-surface k-create-surface--guided" aria-label="Create agent" :aria-busy="busy" @submit.prevent="submit">
        <div class="k-create-body k-create-body--guided">
          <div class="k-create-fields">
            <fieldset class="agents-cap-fs agents-template-fs" aria-describedby="agent-create-template-hint">
              <legend>Start from</legend>
              <div v-if="templatesError" class="k-inline-notification k-inline-notification--warning" role="status">
                <span class="k-inline-notification__body"><span class="k-inline-notification__message">Could not load templates. {{ templatesError }}</span></span>
                <button class="k-inline-notification__action" type="button" :disabled="busy" @click="loadTemplates">Retry</button>
              </div>
              <div class="agents-radiocards agents-template-cards">
                <label class="agents-radiocard k-checkbox-hit" :class="{ sel: template === '' }">
                  <input v-model="template" type="radio" name="agent-create-template" value="" :disabled="busy" />
                  <span class="agents-radiocard-t">Blank</span><span class="agents-radiocard-b">Write the instructions and pick capabilities yourself.</span>
                </label>
                <label v-for="item in templates" :key="item.metadata.name" class="agents-radiocard k-checkbox-hit" :class="{ sel: item.metadata.name === template }">
                  <input v-model="template" type="radio" name="agent-create-template" :value="item.metadata.name" :disabled="busy" />
                  <span class="agents-radiocard-t">{{ item.spec.displayName }}</span>
                  <span class="agents-radiocard-b">{{ item.spec.description }}<span class="agents-template-needs">Needs: {{ templateNeeds(item) }}</span></span>
                </label>
              </div>
              <span id="agent-create-template-hint" class="agents-hint">A template sets the instructions, autonomy, capabilities and schedules; you wire in what it needs from this workspace.</span>
            </fieldset>

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

            <fieldset class="agents-cap-fs" aria-describedby="agent-create-backend-hint">
              <legend>Backend</legend>
              <div class="agents-radiocards">
                <label v-for="option in BACKENDS" :key="option.id" class="agents-radiocard k-checkbox-hit" :class="{ sel: option.id === backendType }">
                  <input v-model="backendType" type="radio" name="agent-create-backend" :value="option.id" :disabled="busy || !!selectedTemplate" />
                  <span class="agents-radiocard-t">{{ option.label }}</span><span class="agents-radiocard-b">{{ option.blurb }}</span>
                </label>
              </div>
              <span id="agent-create-backend-hint" class="agents-hint">{{ selectedTemplate ? `Set by the ${selectedTemplate.spec.displayName} template.` : 'Where this agent’s turns execute.' }}</span>
            </fieldset>

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
                <label>
                  <span id="agent-create-edge-label">Machine *</span>
                  <FormSelect
                    id="agent-create-edge"
                    v-model="harnessEdge"
                    name="harnessEdge"
                    :options="edgeOptions"
                    :disabled="busy"
                    :required="true"
                    :invalid="Boolean(errors.harnessEdge)"
                    labelledby="agent-create-edge-label"
                    :describedby="['agent-create-edge-hint', hostEdges.length === 0 ? 'agent-create-edge-empty' : '', errors.harnessEdge ? 'agent-create-edge-error' : '', edgeSlice.error ? 'agent-create-edge-stale' : ''].filter(Boolean).join(' ')"
                  />
                  <span v-if="errors.harnessEdge" id="agent-create-edge-error" class="agents-fielderr" role="alert">{{ errors.harnessEdge }}</span>
                  <span id="agent-create-edge-hint" class="agents-hint">Linux and macOS machines only — a Kubernetes cluster cannot run a harness process.</span>
                  <span v-if="hostEdges.length === 0" id="agent-create-edge-empty" class="agents-hint">
                    Connect a Linux or macOS machine in Edges, then check again here. Keep this tab open to retain your agent draft.
                  </span>
                </label>
                <div v-if="hostEdges.length === 0" class="agents-form-actions">
                  <a :href="connectMachineHref" target="_blank" rel="noopener noreferrer" class="k-btn k-btn--ghost">Connect a machine (new tab)</a>
                  <button type="button" class="k-btn k-btn--ghost" :disabled="busy || edgeSlice.loading" @click="store.load('edges')">{{ edgeSlice.loading ? 'Checking machines…' : 'Check again' }}</button>
                </div>

                <label>
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
                    :describedby="['agent-create-harnesscred-hint', harnessCredentials.length === 0 ? 'agent-create-harnesscred-empty' : '', errors.harnessCredential ? 'agent-create-harnesscred-error' : ''].filter(Boolean).join(' ')"
                  />
                  <span v-if="errors.harnessCredential" id="agent-create-harnesscred-error" class="agents-fielderr" role="alert">{{ errors.harnessCredential }}</span>
                  <span id="agent-create-harnesscred-hint" class="agents-hint">{{ selectedHarness ? `Runs ${selectedHarness} — decided by this credential’s provider.` : 'A claude-code credential means Claude Code; a codex one means Codex.' }}</span>
                  <span v-if="harnessCredentials.length === 0" id="agent-create-harnesscred-empty" class="agents-hint">
                    No harness identities yet —
                    <button type="button" class="k-dashboard-action" :disabled="busy" @click="addCredential('harness')">
                      add a Claude Code or Codex one under Models
                    </button>
                    first.
                  </span>
                </label>

                <label>
                  <span id="agent-create-harness-model-label">Model</span>
                  <input id="agent-create-harness-model" v-model="harnessModel" class="k-input" placeholder="Harness default" :disabled="busy" aria-labelledby="agent-create-harness-model-label" aria-describedby="agent-create-harness-model-hint" />
                  <span id="agent-create-harness-model-hint" class="agents-hint">Optional — blank leaves the harness’s own default.</span>
                </label>
                <label>
                  <span id="agent-create-harness-effort-label">Reasoning effort</span>
                  <input id="agent-create-harness-effort" :disabled="busy" pattern="[a-z][a-z0-9_-]*" v-model="harnessReasoningEffort" class="k-input" placeholder="Harness default" maxlength="32" aria-labelledby="agent-create-harness-effort-label" aria-describedby="agent-create-harness-effort-hint" />
                  <span id="agent-create-harness-effort-hint" class="agents-hint">Codex only. Enter an effort supported by the selected model, such as max. Leave blank for the harness default.</span>
                </label>

                <label>
                  <span id="agent-create-workspace-label">Working directory</span>
                  <FormSelect v-model="harnessWorkspace" :options="workspaceOptions" :disabled="busy" labelledby="agent-create-workspace-label" />
                </label>

                <label>
                  <span id="agent-create-github-label">GitHub connection{{ githubSlot?.required ? ' *' : '' }}</span>
                  <FormSelect id="agent-create-github" v-model="harnessGitHubConnection" name="harnessGitHubConnection" :options="githubConnectionOptions" :disabled="busy" :required="!!githubSlot?.required" :invalid="Boolean(errors.harnessGitHubConnection)" labelledby="agent-create-github-label" :describedby="['agent-create-github-hint', errors.harnessGitHubConnection ? 'agent-create-github-error' : ''].filter(Boolean).join(' ')" />
                  <span v-if="errors.harnessGitHubConnection" id="agent-create-github-error" class="agents-fielderr" role="alert">{{ errors.harnessGitHubConnection }}</span>
                  <span id="agent-create-github-hint" class="agents-hint">{{ githubSlot?.description || 'Optional — its token reaches the harness as GH_TOKEN for each turn, never the machine, so `gh` can read and review pull requests.' }}</span>
                </label>
              </template>
            </template>

            <template v-else>
            <label>
              <span id="agent-create-model-label">Model credential *</span>
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
                :describedby="['agent-create-model-hint', credentialOptions.length === 0 ? 'agent-create-model-empty' : '', errors.modelCredential ? 'agent-create-model-error' : ''].filter(Boolean).join(' ')"
              />
              <span v-if="errors.modelCredential" id="agent-create-model-error" class="agents-fielderr" role="alert">{{ errors.modelCredential }}</span>
              <span id="agent-create-model-hint" class="agents-hint">The credential and model endpoint used for every turn.</span>
              <span v-if="credentialOptions.length === 0" id="agent-create-model-empty" class="agents-hint">
                No model credentials yet —
                <button type="button" class="k-dashboard-action" :disabled="busy" @click="addCredential('chat')">
                  add one under Models
                </button>
                first.
              </span>
            </label>

            <label v-for="slot in connectionSlots" :key="slot.key">
              <span :id="`agent-create-slot-${slot.key}-label`">{{ slotLabel(slot) }}{{ slot.required ? ' *' : '' }}</span>
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
              <span :id="`agent-create-slot-${slot.key}-hint`" class="agents-hint">{{ slot.description || (slot.required ? '' : 'Optional.') }}</span>
            </label>
            </template>

            <label v-for="param in templateParameters" :key="param.key">
              <span :id="`agent-create-param-${param.key}-label`">{{ param.label }}{{ param.required ? ' *' : '' }}</span>
              <textarea
                v-if="param.type !== 'string'"
                :id="`agent-create-param-${param.key}`"
                v-model="parameters[param.key]"
                class="k-input"
                :rows="param.type === 'list' ? 3 : 2"
                :placeholder="param.default || (param.type === 'list' ? 'One per line' : '')"
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
              <span :id="`agent-create-param-${param.key}-hint`" class="agents-hint">{{ param.description }}{{ param.type === 'list' ? ' One per line.' : '' }}{{ param.default && param.type !== 'string' ? ' Leave blank for the default.' : '' }}</span>
            </label>

            <label v-if="!selectedTemplate || customPrompt">
              <span id="agent-create-system-prompt-label">System prompt</span>
              <textarea v-model="systemPrompt" class="k-input" :rows="customPrompt ? 10 : 3" placeholder="You are a concise assistant that…" :disabled="busy" aria-labelledby="agent-create-system-prompt-label" aria-describedby="agent-create-system-prompt-hint" />
              <span id="agent-create-system-prompt-hint" class="agents-hint">{{ customPrompt ? 'Your edited copy replaces the template’s instructions.' : 'Optional — persona and standing instructions, not mechanics.' }}</span>
            </label>
            <div v-else class="agents-template-prompt">
              <span id="agent-create-prompt-preview-label" class="agents-field-label">Instructions from template</span>
              <pre class="agents-template-prompt-text" aria-labelledby="agent-create-prompt-preview-label" aria-describedby="agent-create-prompt-preview-hint">{{ promptPreview }}</pre>
              <div class="agents-form-actions">
                <button type="button" class="k-btn k-btn--ghost" :disabled="busy" @click="customizePrompt">Customize instructions</button>
              </div>
              <span id="agent-create-prompt-preview-hint" class="agents-hint">Rendered from your answers above. Editable here, or later in Config.</span>
            </div>

            <label>
              <span id="agent-create-channel-label">Primary channel{{ channelSlot?.required ? ' *' : '' }}</span>
              <FormSelect id="agent-create-channel" v-model="channel" :options="channelOptions" :disabled="busy" :required="!!channelSlot?.required" :invalid="Boolean(errors.channel)" labelledby="agent-create-channel-label" :describedby="['agent-create-channel-hint', errors.channel ? 'agent-create-channel-error' : ''].filter(Boolean).join(' ')" />
              <span v-if="errors.channel" id="agent-create-channel-error" class="agents-fielderr" role="alert">{{ errors.channel }}</span>
              <span id="agent-create-channel-hint" class="agents-hint">{{ channelSlot?.description || 'Optional — where this agent messages you.' }}</span>
            </label>

            <p v-if="isHarness" class="agents-hint">
              A coding harness uses its own tools; configure those on the selected machine. Agent tool grants do not apply.
            </p>

            <p v-else-if="selectedTemplate" class="agents-hint">
              This template grants: {{ capabilities }}. Changeable later in Config.
            </p>

            <fieldset v-else class="agents-cap-fs" aria-describedby="agent-create-capabilities-hint">
              <legend>Can do</legend>
              <div class="agents-cap-row">
                <label class="agents-cap k-checkbox-hit">
                  <input v-model="web" type="checkbox" :disabled="busy" />
                  <span class="agents-check-copy"><strong>Read the web</strong><span class="muted">Fetch pages; search needs a websearch tool.</span></span>
                </label>
                <label class="agents-check agents-bg-toggle k-checkbox-hit" title="Background runs have no human watching, so a capability stays interactive-only unless opted in here.">
                  <input v-model="webBackground" type="checkbox" :disabled="busy || !web" /><Clock :stroke-width="1.75" aria-hidden="true" /> background
                </label>
              </div>
              <div class="agents-cap-row">
                <label class="agents-cap k-checkbox-hit">
                  <input v-model="fanOut" type="checkbox" :disabled="busy" />
                  <span class="agents-check-copy"><strong>Research fan-out</strong><span class="muted">Work independent parts in parallel.</span></span>
                </label>
                <label class="agents-check agents-bg-toggle k-checkbox-hit" title="Background runs have no human watching, so a capability stays interactive-only unless opted in here.">
                  <input v-model="fanOutBackground" type="checkbox" :disabled="busy || !fanOut" /><Clock :stroke-width="1.75" aria-hidden="true" /> background
                </label>
              </div>
              <div class="agents-cap-row">
                <label class="agents-cap k-checkbox-hit">
                  <input v-model="visualization" type="checkbox" :disabled="busy" />
                  <span class="agents-check-copy"><strong>Visualize data</strong><span class="muted">Create charts in chat from supplied data.</span></span>
                </label>
                <label class="agents-check agents-bg-toggle k-checkbox-hit">
                  <input v-model="visualizationBackground" type="checkbox" :disabled="busy || !visualization" /><Clock :stroke-width="1.75" aria-hidden="true" /> background
                </label>
              </div>
              <span id="agent-create-capabilities-hint" class="agents-hint">Changeable later.</span>
            </fieldset>
          </div>

          <CreateGuidance
            title="Prepare a usable agent"
            :description="guidanceDescription"
            :prerequisites="prerequisites"
            :values="summaryValues"
            :next-steps="guidanceNextSteps"
          >
            <template #icon><Bot aria-hidden="true" /></template>
          </CreateGuidance>
        </div>

        <div v-if="errors.template" class="k-inline-notification k-inline-notification--error" role="alert">
          <span class="k-inline-notification__body"><span class="k-inline-notification__message">{{ errors.template }}</span></span>
        </div>
        <div class="k-create-actions">
          <button type="button" class="k-btn k-btn--ghost secondary" :disabled="busy" @click="cancel">Cancel</button>
          <button class="k-btn k-btn--primary" type="submit" :disabled="busy || !canSubmit">
            <Check aria-hidden="true" /> {{ busy ? 'Creating…' : 'Create agent' }}
          </button>
        </div>
      </form>
    </template>
  </div>
</template>
