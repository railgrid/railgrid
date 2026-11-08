<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref, watch } from 'vue'
import { ArrowLeft, Check, LayoutTemplate, X } from 'lucide-vue-next'
import AgentTemplateCards from '../components/AgentTemplateCards.vue'
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
  templateFamilies,
  templateSlotLabel as slotLabel,
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
const template = ref(initialDraft.template || props.initialTemplate || '')
const templatesLoading = ref(false)
const templatesLoaded = ref(false)
const parameters = ref<Record<string, string>>({ ...initialDraft.parameters })
// Connection-slot wiring, by slot key. The other slot kinds reuse the fields
// below (model credential, channel, machine, harness identity, GitHub).
const slotWiring = ref<Record<string, string>>({ ...initialDraft.wiring })
// Template instructions track the parameter values until the user edits them.
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
const templatePicker = ref<HTMLDialogElement | null>(null)

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
const promptPreview = computed(() => selectedTemplate.value ? previewPrompt(selectedTemplate.value, parameters.value) : '')
watch(selectedTemplate, (current, previous) => {
  if (!current) return
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
watch([chatCredentials, credentials], ([models, slice]) => {
  if (modelCredential.value || !slice.hasSnapshot || slice.error) return
  const eligible = models.filter(model => !credentialUnavailable(model))
  if (eligible.length === 1 && eligible[0].ready === true) modelCredential.value = eligible[0].name
}, { immediate: true })

function chooseBackend(type: AgentBackendType): void {
  if (type === backendType.value) return
  clearTemplate()
  backendType.value = type
}

function clearTemplate(): void {
  template.value = ''
  parameters.value = {}
  slotWiring.value = {}
  customPrompt.value = false
}

function chooseTemplate(value: string): void {
  template.value = value
  templatePicker.value?.close()
  void nextTick(() => nameInput.value?.focus())
}

function openTemplates(): void {
  templatePicker.value?.showModal()
  if (!templatesLoaded.value) void loadTemplates()
}

const channelOptions = computed(() => [
  { value: '', label: '— none —' },
  ...channels.value.map(item => ({
    value: item.metadata.name,
    label: `${item.spec.displayName || item.metadata.name} (${item.spec.type})`,
  })),
])
const capabilities = computed(() => selectedTemplate.value ? templateFamilies(selectedTemplate.value).join(', ') : '')
// Whether the form can be submitted at all, which is a different question per
// backend: the old condition was "a chat credential exists", which made the
// harness backend unreachable however it was configured.
const canSubmit = computed(() => (!template.value || Boolean(selectedTemplate.value)) && (isHarness.value
  ? hostEdges.value.length > 0 && harnessCredentials.value.length > 0
  : credentialOptions.value.length > 0))

onMounted(() => {
  void nextTick(() => nameInput.value?.focus())
  if (template.value) void loadTemplates()
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
      customPrompt: customPrompt.value,
      channel: channel.value,
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
  } else if (!credentialOptions.value.some(option => option.value === modelCredential.value)) {
    errors.modelCredential = 'Pick the model this agent reasons with.'
  }
  const chosen = selectedTemplate.value
  if (template.value && !chosen) errors.template = 'Load the selected template before creating this agent.'
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
    instance = instantiateTemplate(chosen, { name: normalizedName, parameters: parameters.value, wiring })
  } catch (e) {
    errors.template = (e as Error).message
    return
  }
  if (customPrompt.value) instance.agent.spec.systemPrompt = systemPrompt.value.trim() || undefined
  const harness = instance.agent.spec.backend?.harness
  if (harness) {
    if (harnessModel.value.trim()) harness.model = harnessModel.value.trim()
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
    return `${schedule.spec.suspend ? 'Paused schedule' : 'Schedule'} ${schedule.metadata.name}${when ? ` (${when})` : ''}.`
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
        <p class="k-create-description">Set up an agent you can chat with right away.</p>
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

      <form class="agents-create-form agents-guided-form k-create-surface k-create-surface--guided" aria-label="Create agent" :aria-busy="busy" @submit.prevent="submit">
        <div class="k-create-body k-create-body--guided">
          <div class="k-create-fields">
            <div v-if="selectedTemplate" class="agents-template-selection">
              <p class="agents-hint" data-selected-template>Template: <strong>{{ selectedTemplate.spec.displayName }}</strong></p>
              <button class="k-btn k-btn--text" type="button" :disabled="busy" @click="clearTemplate">Clear template</button>
            </div>
            <div v-else-if="template" class="agents-stale" role="status">
              {{ templatesError ? 'Could not load the selected template.' : templatesLoaded ? 'This template is unavailable.' : 'Loading the selected template…' }}
              <button v-if="templatesError" class="k-btn k-btn--ghost" type="button" @click="loadTemplates">Retry</button>
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

              <label>
                <span id="agent-create-system-prompt-label">Instructions (optional)</span>
                <textarea :value="selectedTemplate && !customPrompt ? promptPreview : systemPrompt" class="k-input" rows="3" placeholder="Describe how this agent should help." :disabled="busy" aria-labelledby="agent-create-system-prompt-label" :aria-describedby="selectedTemplate ? 'agent-create-system-prompt-hint' : undefined" @input="editInstructions" />
                <span v-if="selectedTemplate" id="agent-create-system-prompt-hint" class="agents-hint">{{ customPrompt ? 'Your edits replace the template’s instructions.' : 'Prefilled from the template. Editing makes these instructions your own.' }}</span>
              </label>
            </section>

            <section class="agents-create-section" aria-labelledby="agent-create-execution-heading">
              <h2 id="agent-create-execution-heading">Execution</h2>
              <div class="agents-create-backends" role="group" aria-label="Execution type">
                <button class="k-btn k-btn--ghost" type="button" :aria-pressed="!isHarness" :disabled="busy" @click="chooseBackend(AGENT_BACKEND_MODEL)">Chat model</button>
                <button class="k-btn k-btn--ghost" type="button" data-use-coding-harness :aria-pressed="isHarness" :disabled="busy" @click="chooseBackend(AGENT_BACKEND_HARNESS)">Coding harness</button>
              </div>
              <p v-if="selectedTemplate" class="agents-hint">Changing execution type clears the template.</p>
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
                        add a Claude Code or Codex one under Models → Provider connections
                      </button>
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

                </template>
              </template>

              <template v-else>
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
                <span id="agent-create-model-hint" class="agents-hint">{{ chatCredentials.some(model => model.name === modelCredential && model.ready === true) ? 'Ready to use' : 'The default model for this agent.' }}</span>
              </label>
              <p v-if="credentialOptions.length === 0" id="agent-create-model-empty" class="agents-hint">
                This workspace needs a chat model.
                <button type="button" class="k-dashboard-action" :disabled="busy" @click="addCredential('chat')">Connect a model</button>
              </p>
              </template>
            </section>

            <section class="agents-create-section" aria-labelledby="agent-create-inputs-heading">
              <h2 id="agent-create-inputs-heading">Template inputs</h2>
              <p v-if="!templateParameters.length" class="agents-hint">No template inputs to fill in.</p>
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
            </section>

            <section class="agents-create-section" aria-labelledby="agent-create-connections-heading">
              <h2 id="agent-create-connections-heading">Connections & automation</h2>
              <p v-if="!isHarness && !channelSlot && !connectionSlots.length" class="agents-hint">Add connections or automation after creating the agent.</p>
              <label v-if="isHarness">
                <span id="agent-create-github-label">GitHub connection{{ githubSlot?.required ? ' *' : '' }}</span>
                <FormSelect id="agent-create-github" v-model="harnessGitHubConnection" name="harnessGitHubConnection" :options="githubConnectionOptions" :disabled="busy" :required="!!githubSlot?.required" :invalid="Boolean(errors.harnessGitHubConnection)" labelledby="agent-create-github-label" :describedby="['agent-create-github-hint', errors.harnessGitHubConnection ? 'agent-create-github-error' : ''].filter(Boolean).join(' ')" />
                <span v-if="errors.harnessGitHubConnection" id="agent-create-github-error" class="agents-fielderr" role="alert">{{ errors.harnessGitHubConnection }}</span>
                <span id="agent-create-github-hint" class="agents-hint">{{ githubSlot?.description || 'Optional — its token reaches the harness as GH_TOKEN for each turn, never the machine, so `gh` can read and review pull requests.' }}</span>
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
              <label v-if="channelSlot">
                <span id="agent-create-channel-label">Primary channel{{ channelSlot?.required ? ' *' : '' }}</span>
                <FormSelect id="agent-create-channel" v-model="channel" :options="channelOptions" :disabled="busy" :required="!!channelSlot?.required" :invalid="Boolean(errors.channel)" labelledby="agent-create-channel-label" :describedby="['agent-create-channel-hint', errors.channel ? 'agent-create-channel-error' : ''].filter(Boolean).join(' ')" />
                <span v-if="errors.channel" id="agent-create-channel-error" class="agents-fielderr" role="alert">{{ errors.channel }}</span>
                <span id="agent-create-channel-hint" class="agents-hint">{{ channelSlot?.description || 'Optional — where this agent messages you.' }}</span>
              </label>
              <p v-if="isHarness" class="agents-hint">A coding harness uses its own tools, configured on the selected machine.</p>
              <p v-else-if="capabilities" class="agents-hint">Template capabilities: {{ capabilities }}.</p>
              <p v-if="selectedTemplate?.spec.schedules?.length" class="agents-hint">
                Template schedules: {{ selectedTemplate.spec.schedules.map(schedule => schedule.name).join(', ') }}.
                You’ll review them before creating the agent.
              </p>
            </section>
          </div>

          <CreateGuidance
            title="After you create the agent"
            description="A conversation opens as soon as the agent is created. You can manage tools, channels, and automation from the workbench."
          />
        </div>

        <div v-if="errors.template" class="k-inline-notification k-inline-notification--error" role="alert">
          <span class="k-inline-notification__body"><span class="k-inline-notification__message">{{ errors.template }}</span></span>
        </div>
        <div class="k-create-actions">
          <button class="k-btn k-btn--primary" type="submit" :disabled="busy || !canSubmit">
            <Check aria-hidden="true" /> {{ busy ? 'Creating…' : 'Create & open chat' }}
          </button>
        </div>
      </form>
    </template>
  </div>
</template>
