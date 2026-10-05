<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref, watch } from 'vue'
import { ArrowLeft, Bot, Check, Clock } from 'lucide-vue-next'
import FormSelect, { type FormSelectOption } from '../portalkit/FormSelect.vue'
import CreateGuidance from '../portalkit/CreateGuidance.vue'
import { portalHref } from '../portalkit/navigation'
import { mutate } from '../mutate'
import type { ApiClient } from '../api'
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
}>(), { authorityEpoch: 0, createSession: 0, initialDraft: null, initialBackendType: AGENT_BACKEND_MODEL })
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

const initialDraft = props.initialDraft || emptyAgentCreateDraft(props.initialBackendType)
const name = ref(initialDraft.name)
const backendType = ref<AgentBackendType>(initialDraft.backendType)
const modelCredential = ref(initialDraft.modelCredential)
const harnessEdge = ref(initialDraft.harnessEdge)
const harnessCredential = ref(initialDraft.harnessCredential)
const harnessModel = ref(initialDraft.harnessModel)
const harnessWorkspace = ref<HarnessWorkspace>(initialDraft.harnessWorkspace)
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
const credentialOptions = computed(() => chatCredentials.value.map(item => ({
  value: item.name,
  label: `${item.name}${item.model ? ` (${item.model})` : ''}`,
})))
const channelOptions = computed(() => [
  { value: '', label: '— none —' },
  ...channels.value.map(item => ({
    value: item.metadata.name,
    label: `${item.spec.displayName || item.metadata.name} (${item.spec.type})`,
  })),
])
const capabilities = computed(() => isHarness.value ? 'The harness’s own tools' : [
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
])
const guidanceDescription = computed(() => isHarness.value
  ? 'Choose the machine and harness identity that will run the agent’s turns.'
  : 'Choose the model connection the agent will use for its turns.')
const guidanceNextSteps = computed(() => [
  'Railgrid creates the agent and opens its Config workspace.',
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

onMounted(() => { void nextTick(() => nameInput.value?.focus()) })

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
      harnessWorkspace: harnessWorkspace.value,
      systemPrompt: systemPrompt.value,
      channel: channel.value,
      web: web.value,
      fanOut: fanOut.value,
      visualization: visualization.value,
      webBackground: webBackground.value,
      fanOutBackground: fanOutBackground.value,
      visualizationBackground: visualizationBackground.value,
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
  if (Object.keys(errors).length) return

  const body: AgentCreateBody = { name: normalizedName, displayName: normalizedName }
  if (isHarness.value) {
    const edge = splitEdgeKey(harnessEdge.value)
    body.backendType = AGENT_BACKEND_HARNESS
    body.harness = {
      edgeRef: { kind: edge.kind as 'LinuxServer' | 'MacOSServer', name: edge.name },
      credentialRef: harnessCredential.value,
      ...(harnessModel.value.trim() ? { model: harnessModel.value.trim() } : {}),
      workspace: harnessWorkspace.value,
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
</script>

<template>
  <div class="k-create-page">
    <button type="button" class="k-btn k-btn--ghost k-back-action" :disabled="busy" @click="cancel">
      <ArrowLeft aria-hidden="true" /> Agents
    </button>
    <header class="k-create-header">
      <h1 class="k-create-title">Create agent</h1>
      <p class="k-create-description">Choose where this agent’s turns run, set its instructions, and optionally connect a channel.</p>
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
            <label for="agent-create-name">
              Name *
              <input
                id="agent-create-name"
                ref="nameInput"
                v-model="name"
                class="k-input"
                name="name"
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
                  <input v-model="backendType" type="radio" name="agent-create-backend" :value="option.id" :disabled="busy" />
                  <span class="agents-radiocard-t">{{ option.label }}</span><span class="agents-radiocard-b">{{ option.blurb }}</span>
                </label>
              </div>
              <span id="agent-create-backend-hint" class="agents-hint">Where this agent’s turns execute.</span>
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
                  <span id="agent-create-workspace-label">Working directory</span>
                  <FormSelect v-model="harnessWorkspace" :options="workspaceOptions" :disabled="busy" labelledby="agent-create-workspace-label" />
                </label>
              </template>
            </template>

            <label v-else>
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

            <label>
              <span id="agent-create-system-prompt-label">System prompt</span>
              <textarea v-model="systemPrompt" class="k-input" rows="3" placeholder="You are a concise assistant that…" :disabled="busy" aria-labelledby="agent-create-system-prompt-label" aria-describedby="agent-create-system-prompt-hint" />
              <span id="agent-create-system-prompt-hint" class="agents-hint">Optional — persona and standing instructions, not mechanics.</span>
            </label>

            <label>
              <span id="agent-create-channel-label">Primary channel</span>
              <FormSelect v-model="channel" :options="channelOptions" :disabled="busy" labelledby="agent-create-channel-label" describedby="agent-create-channel-hint" />
              <span id="agent-create-channel-hint" class="agents-hint">Optional — where this agent messages you.</span>
            </label>

            <p v-if="isHarness" class="agents-hint">
              A coding harness uses its own tools; configure those on the selected machine. Agent tool grants do not apply.
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
