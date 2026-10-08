<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch, watchEffect } from 'vue'
import {
  ArrowLeftRight,
  Check,
  Circle,
  Clock,
  Globe2,
  Plus,
  Puzzle,
  Send,
  Server,
  Star,
  Trash2,
  Wrench,
  X,
} from 'lucide-vue-next'
import type { ApiClient } from '../api'
import { validateBudgetInputs } from '../budget-validation'
import ConfigSaveFeedback, { type ConfigSaveStatus } from '../components/ConfigSaveFeedback.vue'
import SecretHandoff from '../components/SecretHandoff.vue'
import { channelInbound } from '../conn-defs'
import { mutate } from '../mutate'
import FormSelect, { type FormSelectOption } from '../portalkit/FormSelect.vue'
import { portalHref } from '../portalkit/navigation'
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { toast } from '../portalkit/toast'
import type { Route } from '../router'
import type { AppStore } from '../store'
import {
  AGENT_BACKEND_HARNESS,
  AGENT_BACKEND_MODEL,
  CONDITION_BACKEND_READY,
  HARNESS_EDGE_KINDS,
  WORKSPACE_MODES,
  edgeKey,
  splitEdgeKey,
  agentBackendType,
  agentCondition,
  agentHarness,
  agentHarnessBacked,
  agentModelCredential,
  agentModelFallbacks,
  harnessLabel,
  type Agent,
  type AgentBackendType,
  type AgentChannel,
  type AgentHarnessBackend,
  type AgentPatch,
  type Autonomy,
  type HarnessWorkspace,
} from '../types'
import { queueAgentConfigSave } from '../vue/config-save-queue'
import { useAuthorityGuard, useStoreRevision, type AuthoritySnapshot } from '../vue/runtime'
import Automation from './Automation.vue'

const AUTONOMY_MODES: { id: Autonomy; label: string; blurb: string }[] = [
  { id: 'suggest', label: 'Suggest', blurb: 'Consequential tool calls wait for approval; memory, ask, wait, notify, and schedule-list built-ins remain available.' },
  { id: 'ask', label: 'Ask', blurb: 'Only tools matched by a grant’s approval patterns wait for you.' },
  { id: 'auto', label: 'Auto', blurb: 'Tools run without asking. Use only with tools you trust unattended.' },
]

// An agent has exactly ONE backend. The two are not variants of each other: a
// model-backed agent's turns run in the provider against a chat endpoint and use
// the tools granted below; a harness-backed agent's turns run on a machine and
// use the harness's own tools, so no grant here applies to it.
const BACKEND_MODES: { id: AgentBackendType; label: string; blurb: string }[] = [
  { id: AGENT_BACKEND_MODEL, label: 'Model', blurb: 'Uses a model connection and the tools configured for this agent.' },
  { id: AGENT_BACKEND_HARNESS, label: 'Coding harness', blurb: 'Runs on a Linux or macOS machine and uses its own tools. Agent tool grants do not apply.' },
]

interface ChannelRow extends AgentChannel { key: number }

type SaveRegion = 'persona' | 'backend' | 'model' | 'policy' | 'tools' | 'channels' | 'delegates'
type ManualSaveRegion = Exclude<SaveRegion, 'tools' | 'delegates'>

interface PersonaSnapshot {
  displayName: string
  description: string
  systemPrompt: string
}

interface ModelSnapshot {
  modelCredential: string
  modelFallbacks: string[]
}

/**
 * BackendSnapshot is what the backend card owns. `edge` is "Kind/name" in one
 * value on purpose: the kind is an enum of the two HOST edge kinds, so picking
 * the machine and picking its kind is one decision and cannot disagree.
 */
interface BackendSnapshot {
  type: AgentBackendType
  edge: string
  credentialRef: string
  model: string
  workspace: HarnessWorkspace
}

interface PolicySnapshot {
  autonomy: Autonomy
  budgetUSD: string
  budgetTokens: string
  maxToolTurns: number
  timeoutSeconds: number
}

interface SaveRegionState {
  status: ConfigSaveStatus
  error: string
  sequence: number
  submitted: unknown | null
}

const props = withDefaults(defineProps<{
  store: AppStore
  api: ApiClient
  name: string
  authorityEpoch?: number
  /** Move the tools and automation cards into AgentDetail workbench panels. */
  workbenchSections?: boolean
}>(), { authorityEpoch: 0, workbenchSections: false })
const emit = defineEmits<{ navigate: [route: Route] }>()
const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)

const displayName = ref('')
const description = ref('')
const systemPrompt = ref('')
const modelCredential = ref('')
const fallbacks = ref<string[]>([])
const backendType = ref<AgentBackendType>(AGENT_BACKEND_MODEL)
// "Kind/name" — see BackendSnapshot.
const harnessEdge = ref('')
const harnessCredential = ref('')
const harnessModel = ref('')
const harnessWorkspace = ref<HarnessWorkspace>('persistent')
const harnessError = ref('')
const autonomy = ref<Autonomy>('ask')
const budgetUSD = ref('')
const budgetTokens = ref('')
const budgetUSDError = ref('')
const budgetTokensError = ref('')
const maxToolTurns = ref('')
const timeoutSeconds = ref('')
const maxToolTurnsValidation = computed(() => validateOptionalLimit(maxToolTurns.value))
const timeoutSecondsValidation = computed(() => validateOptionalLimit(timeoutSeconds.value))
const channels = ref<ChannelRow[]>([])
const channelError = ref('')
const channelErrorTarget = ref<{ key: number; field: 'name' | 'connection' } | null>(null)
const slackRequestURL = ref('')

let hydratedStore: AppStore | null = null
let hydratedName = ''
let hydratedApi: ApiClient | null = null
let rowKey = 0
let authorityGeneration = 0

const baselines = reactive<{
  persona: PersonaSnapshot | null
  backend: BackendSnapshot | null
  model: ModelSnapshot | null
  policy: PolicySnapshot | null
  channels: AgentChannel[] | null
}>({ persona: null, backend: null, model: null, policy: null, channels: null })

// The queue applies optimistic specs immediately, so dirty state compares the
// draft with the last acknowledged section baseline rather than the live
// agent object. This keeps edits made during an in-flight write visible.
const saveState = reactive<Record<SaveRegion, SaveRegionState>>({
  persona: { status: 'idle', error: '', sequence: 0, submitted: null },
  backend: { status: 'idle', error: '', sequence: 0, submitted: null },
  model: { status: 'idle', error: '', sequence: 0, submitted: null },
  policy: { status: 'idle', error: '', sequence: 0, submitted: null },
  tools: { status: 'idle', error: '', sequence: 0, submitted: null },
  channels: { status: 'idle', error: '', sequence: 0, submitted: null },
  delegates: { status: 'idle', error: '', sequence: 0, submitted: null },
})

const agent = computed(() => {
  revision.value
  return props.store.agent(props.name)
})
const credentialSlice = computed(() => {
  revision.value
  return { ...props.store.credentials }
})
const toolsetSlice = computed(() => {
  revision.value
  return { ...props.store.toolsets }
})
const connectionSlice = computed(() => {
  revision.value
  return { ...props.store.connections }
})
const edgeSlice = computed(() => {
  revision.value
  return { ...props.store.edges }
})
// Two credential families, and the API rejects one where the other belongs: a
// chat endpoint cannot drive a coding harness, and a `claude setup-token` value
// is not a bearer any chat API would accept. So each picker offers only its own.
const credentials = computed(() => {
  revision.value
  return props.store.chatCredentials()
})
const harnessCredentials = computed(() => {
  revision.value
  return props.store.harnessCredentials()
})
const toolsets = computed(() => toolsetSlice.value.data)
const toolConnections = computed(() => {
  revision.value
  return props.store.toolConnections()
})
const channelConnections = computed(() => {
  revision.value
  return props.store.channelConnections()
})
const otherAgents = computed(() => {
  revision.value
  return props.store.agents.data.filter(candidate => candidate.metadata.name !== props.name)
})
const availableFallbacks = computed(() => credentials.value.filter(credential => (
  credential.name !== modelCredential.value && !fallbacks.value.includes(credential.name)
)))
const credentialOptions = computed<FormSelectOption[]>(() => [
  { value: '', label: '— no model —' },
  ...credentials.value.map(credential => ({
    value: credential.name,
    label: `${credential.name}${credential.model ? ` (${credential.model})` : ''}`,
  })),
])
/**
 * hostEdges is what the machine picker may offer: LinuxServer and MacOSServer,
 * and nothing else.
 *
 * The filter is here as well as in the read (resources.listEdges reads only the
 * two host kinds) because this is the last point before a value reaches the
 * form. A runner is a process on a machine, so a KubernetesCluster edge can
 * never host a harness and the CRD's own enum refuses it — offering one would
 * produce a save the apiserver rejects, which is a worse answer than never
 * having offered it.
 */
const hostEdges = computed(() => edgeSlice.value.data.filter(edge => HARNESS_EDGE_KINDS.includes(edge.kind)))
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
const fallbackOptions = computed<FormSelectOption[]>(() => [
  { value: '', label: '+ add fallback…' },
  ...availableFallbacks.value.map(credential => ({ value: credential.name, label: credential.name })),
])
const channelOptions = computed<FormSelectOption[]>(() => [
  { value: '', label: '— pick a connection —' },
  ...channelConnections.value.map(connection => ({
    value: connection.metadata.name,
    label: `${connection.spec.displayName || connection.metadata.name} (${connection.spec.type})`,
  })),
])

function inboundState(connectionRef: string): { found: boolean; on: boolean; canEnable: boolean; note: string } {
  const connection = channelConnections.value.find(candidate => candidate.metadata.name === connectionRef)
  if (!connection) return { found: false, on: false, canEnable: false, note: '' }
  return { found: true, ...channelInbound(connection) }
}

function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T
}

function same(left: unknown, right: unknown): boolean {
  return JSON.stringify(left) === JSON.stringify(right)
}

function personaSnapshot(source: Agent): PersonaSnapshot {
  return {
    displayName: (source.spec?.displayName || source.metadata.name).trim(),
    description: (source.spec?.description || '').trim(),
    systemPrompt: source.spec?.systemPrompt || '',
  }
}

function modelSnapshot(source: Agent): ModelSnapshot {
  return {
    modelCredential: agentModelCredential(source),
    modelFallbacks: [...agentModelFallbacks(source)],
  }
}

/**
 * backendSnapshot reads the stored backend. An agent with no spec.backend at all
 * is a MODEL-backed agent — that is what it always was, and rendering it as an
 * empty state would invite someone to re-pick a backend it already has.
 */
function backendSnapshot(source: Agent): BackendSnapshot {
  const harness = agentHarness(source)
  return {
    type: agentBackendType(source),
    edge: edgeKey(harness?.edgeRef?.kind || '', harness?.edgeRef?.name || ''),
    credentialRef: harness?.credentialRef || '',
    model: harness?.model || '',
    workspace: harness?.workspace || 'persistent',
  }
}

function policySnapshot(source: Agent): PolicySnapshot {
  return {
    autonomy: (source.spec?.autonomy as Autonomy) || 'ask',
    budgetUSD: source.spec?.budget?.usdLimit || '',
    budgetTokens: source.spec?.budget?.tokenLimit ? String(source.spec.budget.tokenLimit) : '',
    maxToolTurns: source.spec?.limits?.maxToolTurns || 0,
    timeoutSeconds: source.spec?.limits?.timeoutSeconds || 0,
  }
}

function channelSnapshot(rows: AgentChannel[]): AgentChannel[] {
  return rows
    .map(row => ({ name: row.name.trim(), connectionRef: row.connectionRef.trim(), primary: Boolean(row.primary) }))
    .filter(row => row.name || row.connectionRef)
}

const personaDraft = computed<PersonaSnapshot>(() => ({
  displayName: displayName.value.trim(),
  description: description.value.trim(),
  systemPrompt: systemPrompt.value,
}))
const modelDraft = computed<ModelSnapshot>(() => ({
  modelCredential: modelCredential.value,
  modelFallbacks: [...fallbacks.value],
}))
const backendDraft = computed<BackendSnapshot>(() => ({
  type: backendType.value,
  edge: backendType.value === AGENT_BACKEND_HARNESS ? harnessEdge.value : '',
  credentialRef: backendType.value === AGENT_BACKEND_HARNESS ? harnessCredential.value : '',
  model: backendType.value === AGENT_BACKEND_HARNESS ? harnessModel.value.trim() : '',
  workspace: backendType.value === AGENT_BACKEND_HARNESS ? harnessWorkspace.value : 'persistent',
}))
const policyDraft = computed<PolicySnapshot>(() => {
  const budget = validateBudgetInputs(budgetUSD.value, budgetTokens.value)
  return {
    autonomy: autonomy.value,
    budgetUSD: budget.usdError ? budgetUSD.value.trim() : budget.budgetUSD,
    budgetTokens: budget.tokenError ? budgetTokens.value.trim() : budget.budgetTokens ? String(budget.budgetTokens) : '',
    maxToolTurns: maxToolTurnsValidation.value.value,
    timeoutSeconds: timeoutSecondsValidation.value.value,
  }
})
const channelsDraft = computed(() => channelSnapshot(channels.value))
const personaDirty = computed(() => !same(personaDraft.value, baselines.persona))
const modelDirty = computed(() => !same(modelDraft.value, baselines.model))
const backendDirty = computed(() => !same(backendDraft.value, baselines.backend))
// The model fields belong to the model backend, so the draft choice — not the
// stored one — decides whether they are shown. Picking "harness" has to hide
// them at once: the API rejects the block that does not match the type, and a
// visible credential field on a harness-backed agent would promise otherwise.
const modelBacked = computed(() => backendType.value === AGENT_BACKEND_MODEL)
// Tool grants are the stored agent's, so what disables the tools card is what
// the object says today — not an unsaved draft.
const harnessStored = computed(() => agentHarnessBacked(agent.value))
/** backendCondition is the reconciler's verdict on whether a turn can run. */
const backendCondition = computed(() => agentCondition(agent.value, CONDITION_BACKEND_READY))
const resolvedHarness = computed(() => {
  const backend = agent.value?.status?.backend
  if (!backend?.harness?.name) return ''
  const version = backend.harness.version
  return version ? `${backend.harness.name} ${version}` : backend.harness.name
})
const policyDirty = computed(() => !same(policyDraft.value, baselines.policy) || policyLimitInvalid.value)
const channelsDirty = computed(() => !same(channelsDraft.value, baselines.channels))
const harnessPolicyTarget = computed(() => backendType.value === AGENT_BACKEND_HARNESS)
const hasToolTurnLimit = computed(() => maxToolTurnsValidation.value.value > 0)
const hasToolTurnDraft = computed(() => maxToolTurns.value.trim() !== '')
const policyLimitInvalid = computed(() => Boolean(maxToolTurnsValidation.value.error || timeoutSecondsValidation.value.error))
const harnessPolicyIssues = computed(() => {
  if (!harnessPolicyTarget.value) return []
  const issues: string[] = []
  if (autonomy.value !== 'ask') issues.push('Choose Ask and save the policy.')
  if (maxToolTurnsValidation.value.error) issues.push(`Max tool turns: ${maxToolTurnsValidation.value.error}`)
  if (hasToolTurnLimit.value) issues.push('Clear Max tool turns and save the policy.')
  return issues
})
const savedPolicyBlocksHarness = computed(() => Boolean(
  baselines.policy && (baselines.policy.autonomy !== 'ask' || baselines.policy.maxToolTurns > 0),
))
const backendPolicyBlocker = computed(() => {
  if (!harnessPolicyTarget.value) return ''
  if (harnessPolicyIssues.value.length) return harnessPolicyIssues.value.join(' ')
  if (savedPolicyBlocksHarness.value) return 'Save the updated policy before saving the harness backend.'
  return ''
})
const policyDescription = computed(() => harnessPolicyTarget.value
  ? 'The coding harness owns its permission prompts on the selected machine. Railgrid still enforces monthly usage caps and the run timeout.'
  : 'Autonomy controls approval for provider-managed tools. Paused actions appear in Activity before they run.')

function autonomyBlurb(mode: Autonomy): string {
  if (!harnessPolicyTarget.value) return AUTONOMY_MODES.find(item => item.id === mode)?.blurb || ''
  if (mode === 'ask') return 'The runner controls its own permission prompts on the selected machine.'
  return 'Unavailable for coding harnesses; permission behavior belongs to the runner.'
}

function resetSaveState(): void {
  for (const region of Object.keys(saveState) as SaveRegion[]) {
    saveState[region].status = 'idle'
    saveState[region].error = ''
    saveState[region].submitted = null
    saveState[region].sequence += 1
  }
}

function beginSave(region: SaveRegion, submitted: unknown = null): number {
  const state = saveState[region]
  state.sequence += 1
  state.status = 'pending'
  state.error = ''
  state.submitted = submitted === null ? null : clone(submitted)
  return state.sequence
}

function updateBaseline(region: ManualSaveRegion, snapshot: PersonaSnapshot | BackendSnapshot | ModelSnapshot | PolicySnapshot | AgentChannel[]): void {
  if (region === 'persona') baselines.persona = clone(snapshot as PersonaSnapshot)
  if (region === 'backend') baselines.backend = clone(snapshot as BackendSnapshot)
  if (region === 'model') baselines.model = clone(snapshot as ModelSnapshot)
  if (region === 'policy') baselines.policy = clone(snapshot as PolicySnapshot)
  if (region === 'channels') baselines.channels = clone(snapshot as AgentChannel[])
}

function draftFor(region: ManualSaveRegion): PersonaSnapshot | BackendSnapshot | ModelSnapshot | PolicySnapshot | AgentChannel[] {
  if (region === 'persona') return personaDraft.value
  if (region === 'backend') return backendDraft.value
  if (region === 'model') return modelDraft.value
  if (region === 'policy') return policyDraft.value
  return channelsDraft.value
}

function feedbackStatus(region: SaveRegion, dirty = false): ConfigSaveStatus {
  const state = saveState[region]
  if (state.status === 'pending') return 'pending'
  if (state.status === 'error') return 'error'
  if (dirty) return 'dirty'
  return state.status
}

function feedbackError(region: SaveRegion): string | undefined {
  return saveState[region].error || undefined
}

function newerEdits(region: ManualSaveRegion): boolean {
  const state = saveState[region]
  return state.status === 'pending' && state.submitted !== null && (
    !same(state.submitted, draftFor(region)) || (region === 'policy' && policyLimitInvalid.value)
  )
}

function feedbackDescription(region: SaveRegion, dirty = false): string | undefined {
  return feedbackStatus(region, dirty) === 'idle' ? undefined : `agent-${region}-save-feedback`
}

function hydrate(source: Agent): void {
  displayName.value = source.spec?.displayName || source.metadata.name
  description.value = source.spec?.description || ''
  systemPrompt.value = source.spec?.systemPrompt || ''
  modelCredential.value = agentModelCredential(source)
  fallbacks.value = [...agentModelFallbacks(source)]
  const backend = backendSnapshot(source)
  backendType.value = backend.type
  harnessEdge.value = backend.edge
  harnessCredential.value = backend.credentialRef
  harnessModel.value = backend.model
  harnessWorkspace.value = backend.workspace
  harnessError.value = ''
  autonomy.value = (source.spec?.autonomy as Autonomy) || 'ask'
  budgetUSD.value = source.spec?.budget?.usdLimit || ''
  budgetTokens.value = source.spec?.budget?.tokenLimit ? String(source.spec.budget.tokenLimit) : ''
  budgetUSDError.value = ''
  budgetTokensError.value = ''
  maxToolTurns.value = source.spec?.limits?.maxToolTurns ? String(source.spec.limits.maxToolTurns) : ''
  timeoutSeconds.value = source.spec?.limits?.timeoutSeconds ? String(source.spec.limits.timeoutSeconds) : ''
  channels.value = (source.spec?.channels || []).map(channel => ({ ...channel, key: ++rowKey }))
  channelError.value = ''
  channelErrorTarget.value = null
  baselines.persona = personaSnapshot(source)
  baselines.backend = backend
  baselines.model = modelSnapshot(source)
  baselines.policy = policySnapshot(source)
  baselines.channels = channelSnapshot(source.spec?.channels || [])
  resetSaveState()
  authorityGeneration += 1
}

// Store refreshes invalidate the computed values but never overwrite a draft.
// A new store authority or agent identity is a new form and hydrates once.
watchEffect(() => {
  const source = agent.value
  if (!source || (hydratedStore === props.store && hydratedName === props.name && hydratedApi === props.api)) return
  hydrate(source)
  hydratedStore = props.store
  hydratedName = props.name
  hydratedApi = props.api
})

function save(
  patch: AgentPatch,
  apply: (spec: Agent['spec']) => void,
  success: string,
  authority: AuthoritySnapshot = captureAuthority(),
): Promise<boolean> {
  return queueAgentConfigSave({
    store: authority.store,
    api: authority.api,
    name: props.name,
    patch,
    apply,
    success,
  })
}

function saveRegion<T extends PersonaSnapshot | BackendSnapshot | ModelSnapshot | PolicySnapshot | AgentChannel[]>(
  region: ManualSaveRegion,
  snapshot: T,
  patch: AgentPatch,
  apply: (spec: Agent['spec']) => void,
  success: string,
  action: string,
): Promise<boolean> {
  if (saveState[region].status === 'pending') return Promise.resolve(false)
  const authority = captureAuthority()
  const agentName = props.name
  const authorityEpoch = props.authorityEpoch
  const generation = authorityGeneration
  const sequence = beginSave(region, snapshot)
  return save(patch, apply, success, authority).then(saved => {
    // A result from another store, agent, route epoch, or component lifetime
    // can never rewrite this form's feedback or baseline.
    if (!authorityIsCurrent(authority) || generation !== authorityGeneration || props.name !== agentName || props.authorityEpoch !== authorityEpoch) return saved
    if (sequence !== saveState[region].sequence) return saved
    if (saved) {
      updateBaseline(region, snapshot)
      saveState[region].status = same(snapshot, draftFor(region)) ? 'saved' : 'dirty'
      saveState[region].error = ''
      saveState[region].submitted = null
    } else {
      saveState[region].status = 'error'
      saveState[region].error = `Could not save ${action}. Try again.`
      saveState[region].submitted = null
    }
    return saved
  })
}

function savePersona(): void {
  if (saveState.persona.status === 'pending') return
  const nextName = displayName.value.trim()
  const nextDescription = description.value.trim()
  const nextPrompt = systemPrompt.value
  void saveRegion(
    'persona',
    { displayName: nextName, description: nextDescription, systemPrompt: nextPrompt },
    { displayName: nextName, description: nextDescription, systemPrompt: nextPrompt },
    spec => {
      spec.displayName = nextName
      spec.description = nextDescription
      spec.systemPrompt = nextPrompt
    },
    'Persona saved.',
    'persona',
  )
}

function addFallback(value: string): void {
  if (value && !fallbacks.value.includes(value) && value !== modelCredential.value) {
    fallbacks.value = [...fallbacks.value, value]
  }
}

function removeFallback(index: number): void {
  fallbacks.value = fallbacks.value.filter((_, candidate) => candidate !== index)
}

function saveModel(): void {
  if (saveState.model.status === 'pending') return
  const credential = modelCredential.value
  const nextFallbacks = [...fallbacks.value]
  void saveRegion(
    'model',
    { modelCredential: credential, modelFallbacks: nextFallbacks },
    { modelCredential: credential, modelFallbacks: nextFallbacks },
    spec => {
      // Only the model block: the backend TYPE belongs to the backend card, and
      // a section save must not write a field another section owns.
      const backend = spec.backend || (spec.backend = {})
      backend.model = { ...(backend.model || {}), credentials: { ...(backend.model?.credentials || {}), chat: credential }, fallbacks: nextFallbacks }
    },
    'Model saved.',
    'model',
  )
}

/**
 * saveBackend writes spec.backend: the type, and the block that matches it.
 *
 * It clears the other block, because the two are mutually exclusive by CEL and
 * an agent carrying both reads as configured for either. That is also why this
 * is one card and one save rather than two independent ones — you cannot own
 * half of a choice that has to be exactly one thing.
 */
function saveBackend(): void {
  if (saveState.backend.status === 'pending') return
  harnessError.value = ''
  const snapshot = backendDraft.value
  if (snapshot.type === AGENT_BACKEND_HARNESS) {
    if (backendPolicyBlocker.value) {
      harnessError.value = backendPolicyBlocker.value
      return
    }
    const { kind, name } = splitEdgeKey(snapshot.edge)
    if (!kind || !name) {
      harnessError.value = 'Pick the machine this agent’s turns run on.'
      return
    }
    if (!snapshot.credentialRef) {
      harnessError.value = 'Pick a harness credential — its provider is what decides whether this runs Claude Code or Codex.'
      return
    }
    const harness: AgentHarnessBackend = {
      edgeRef: { kind, name },
      credentialRef: snapshot.credentialRef,
      workspace: snapshot.workspace,
      ...(snapshot.model ? { model: snapshot.model } : {}),
    }
    void saveRegion(
      'backend',
      snapshot,
      { backendType: AGENT_BACKEND_HARNESS, harness },
      spec => {
        spec.backend = { type: AGENT_BACKEND_HARNESS, harness }
      },
      'Backend saved.',
      'the backend',
    )
    return
  }
  void saveRegion(
    'backend',
    snapshot,
    { backendType: AGENT_BACKEND_MODEL },
    spec => {
      spec.backend = { ...(spec.backend || {}), type: AGENT_BACKEND_MODEL, harness: undefined }
    },
    'Backend saved.',
    'the backend',
  )
}

function savePolicy(): void {
  if (saveState.policy.status === 'pending') return
  if (harnessPolicyIssues.value.length) return
  if (policyLimitInvalid.value) return
  const budget = validateBudgetInputs(budgetUSD.value, budgetTokens.value)
  budgetUSDError.value = budget.usdError
  budgetTokensError.value = budget.tokenError
  if (budget.usdError || budget.tokenError) return
  const tokens = budget.budgetTokens
  const turns = maxToolTurnsValidation.value.value
  const timeout = timeoutSecondsValidation.value.value
  const usd = budget.budgetUSD
  const mode = autonomy.value
  void saveRegion(
    'policy',
    {
      autonomy: mode,
      budgetUSD: usd,
      budgetTokens: tokens ? String(tokens) : '',
      maxToolTurns: turns,
      timeoutSeconds: timeout,
    },
    { autonomy: mode, budgetUSD: usd, budgetTokens: tokens, maxToolTurns: turns, timeoutSeconds: timeout },
    spec => {
      spec.autonomy = mode
      spec.budget = { ...(spec.budget || {}), usdLimit: usd, tokenLimit: tokens }
      spec.limits = { maxToolTurns: turns, timeoutSeconds: timeout }
    },
    'Autonomy, budget and limits saved.',
    'autonomy, budget and limits',
  )
}

function linkedToolsets(source: Agent): Set<string> {
  return new Set([...(source.spec?.tools?.interactive?.toolsets || []), ...(source.spec?.tools?.background?.toolsets || [])])
}
function backgroundToolsets(source: Agent): Set<string> {
  return new Set(source.spec?.tools?.background?.toolsets || [])
}
function linkedTools(source: Agent): Set<string> {
  return new Set([...(source.spec?.tools?.interactive?.connections || []), ...(source.spec?.tools?.background?.connections || [])])
}
function backgroundTools(source: Agent): Set<string> {
  return new Set(source.spec?.tools?.background?.connections || [])
}
function familyEnabled(source: Agent, family: string, background = false): boolean {
  const grant = background ? source.spec?.tools?.background : source.spec?.tools?.interactive
  return (grant?.families || []).includes(family)
}
function hasSearchTool(source: Agent): boolean {
  return (source.spec?.tools?.interactive?.connections || []).some(name => props.store.connectionType(name) === 'websearch')
}

function saveImmediate(
  region: 'tools' | 'delegates',
  patch: AgentPatch,
  apply: (spec: Agent['spec']) => void,
  success: string,
  action: string,
): void {
  const authority = captureAuthority()
  const agentName = props.name
  const authorityEpoch = props.authorityEpoch
  const generation = authorityGeneration
  const sequence = beginSave(region)
  void save(patch, apply, success, authority).then(saved => {
    if (!authorityIsCurrent(authority) || generation !== authorityGeneration || props.name !== agentName || props.authorityEpoch !== authorityEpoch) return
    if (sequence !== saveState[region].sequence) return
    saveState[region].status = saved ? 'saved' : 'error'
    saveState[region].error = saved ? '' : `Could not save ${action}. Try again.`
  })
}

function setToolsetLinked(source: Agent, toolset: string, on: boolean): void {
  const interactive = source.spec?.tools?.interactive?.toolsets || []
  const background = source.spec?.tools?.background?.toolsets || []
  const nextInteractive = on ? [...new Set([...interactive, toolset])] : interactive.filter(name => name !== toolset)
  const nextBackground = on ? background : background.filter(name => name !== toolset)
  saveImmediate(
    'tools',
    { interactiveToolsets: nextInteractive, backgroundToolsets: nextBackground },
    spec => setGrants(spec, { interactiveToolsets: nextInteractive, backgroundToolsets: nextBackground }),
    on ? 'Toolset linked.' : 'Toolset unlinked.',
    'tool access',
  )
}

function setToolsetBackground(source: Agent, toolset: string, on: boolean): void {
  const background = source.spec?.tools?.background?.toolsets || []
  const next = on ? [...new Set([...background, toolset])] : background.filter(name => name !== toolset)
  saveImmediate(
    'tools',
    { backgroundToolsets: next },
    spec => setGrants(spec, { backgroundToolsets: next }),
    on ? 'Toolset enabled for background runs.' : 'Toolset is now interactive-only.',
    'tool access',
  )
}

function setToolLinked(source: Agent, connection: string, on: boolean): void {
  const interactive = source.spec?.tools?.interactive?.connections || []
  const background = source.spec?.tools?.background?.connections || []
  const nextInteractive = on ? [...new Set([...interactive, connection])] : interactive.filter(name => name !== connection)
  const nextBackground = on ? background : background.filter(name => name !== connection)
  const patch: AgentPatch = {
    interactiveConnections: nextInteractive,
    backgroundConnections: nextBackground,
    interactiveFamilies: props.store.familiesFor(nextInteractive, source.spec?.tools?.interactive?.families),
    backgroundFamilies: props.store.familiesFor(nextBackground, source.spec?.tools?.background?.families),
  }
  saveImmediate('tools', patch, spec => setGrants(spec, patch), on ? 'Tool granted.' : 'Tool removed.', 'tool access')
}

function setToolBackground(source: Agent, connection: string, on: boolean): void {
  const background = source.spec?.tools?.background?.connections || []
  const next = on ? [...new Set([...background, connection])] : background.filter(name => name !== connection)
  const patch: AgentPatch = {
    backgroundConnections: next,
    backgroundFamilies: props.store.familiesFor(next, source.spec?.tools?.background?.families),
  }
  saveImmediate(
    'tools',
    patch,
    spec => setGrants(spec, patch),
    on ? 'Tool enabled for background runs.' : 'Tool is now interactive-only.',
    'tool access',
  )
}

function setFamily(source: Agent, family: string, label: string, on: boolean, background: boolean): void {
  const withFamily = (families: string[] | undefined, enabled: boolean): string[] => {
    const next = new Set(families && families.length ? families : ['core'])
    if (enabled) next.add(family)
    else next.delete(family)
    return [...next]
  }
  const interactiveFamilies = source.spec?.tools?.interactive?.families
  const backgroundFamilies = source.spec?.tools?.background?.families
  const patch: AgentPatch = background
    ? { backgroundFamilies: withFamily(backgroundFamilies, on) }
    : {
        interactiveFamilies: withFamily(interactiveFamilies, on),
        ...(on ? {} : { backgroundFamilies: withFamily(backgroundFamilies, false) }),
      }
  const message = background
    ? on ? `${label} enabled for background runs.` : `${label} is now interactive-only.`
    : on ? `${label} enabled.` : `${label} disabled.`
  saveImmediate('tools', patch, spec => setGrants(spec, patch), message, 'tool access')
}

function patchChannelRow(key: number, patch: Partial<AgentChannel>): void {
  channels.value = channels.value.map(row => row.key === key ? { ...row, ...patch } : row)
  channelError.value = ''
  channelErrorTarget.value = null
}

function setPrimaryChannel(key: number): void {
  channels.value = channels.value.map(row => ({ ...row, primary: row.key === key }))
}

function removeChannel(key: number): void {
  channels.value = channels.value.filter(row => row.key !== key)
  channelError.value = ''
  channelErrorTarget.value = null
}

function nextChannelName(): string {
  const used = new Set(channels.value.map(row => row.name.trim()).filter(Boolean))
  if (!used.has('primary')) return 'primary'
  for (let index = 2; ; index += 1) {
    const candidate = `channel${index}`
    if (!used.has(candidate)) return candidate
  }
}

function addChannel(): void {
  channelError.value = ''
  channelErrorTarget.value = null
  channels.value = [
    ...channels.value,
    { key: ++rowKey, name: nextChannelName(), connectionRef: '', primary: channels.value.length === 0 },
  ]
}

async function saveChannels(): Promise<void> {
  if (saveState.channels.status === 'pending') return
  const trimmed = channels.value.map(row => ({
    key: row.key,
    name: row.name.trim(),
    connectionRef: row.connectionRef.trim(),
    primary: Boolean(row.primary),
  }))
  const rows = trimmed.filter(row => row.name || row.connectionRef)
  for (const row of rows) {
    if (!row.connectionRef) {
      channelError.value = `Channel “${row.name}” has no connection — pick one, or remove the row.`
      channelErrorTarget.value = { key: row.key, field: 'connection' }
      return
    }
    if (!row.name) {
      channelError.value = 'Every channel needs a role name (for example “primary”).'
      channelErrorTarget.value = { key: row.key, field: 'name' }
      return
    }
  }
  const seen = new Set<string>()
  for (const row of rows) {
    if (seen.has(row.name)) {
      channelError.value = `Duplicate channel name “${row.name}” — names must be unique.`
      channelErrorTarget.value = { key: row.key, field: 'name' }
      return
    }
    seen.add(row.name)
  }
  channelError.value = ''
  channelErrorTarget.value = null
  const payload = rows.map(row => ({ name: row.name, connectionRef: row.connectionRef, primary: row.primary }))
  await saveRegion('channels', payload, { channels: payload }, spec => { spec.channels = payload }, 'Channels saved.', 'channels')
}

async function enableInbound(name: string): Promise<void> {
  const authority = captureAuthority()
  const agentName = props.name
  slackRequestURL.value = ''
  const result = await mutate(authority.store, {
    run: () => authority.api.enableInbound(name),
    failure: 'Enable inbound failed',
    reload: ['connections'],
  })
  if (result && authorityIsCurrent(authority) && props.name === agentName) {
    const connection = channelConnections.value.find(item => item.metadata.name === name)
    if (connection?.spec.type === 'slack' && result.webhookURL) slackRequestURL.value = result.webhookURL
    toast(result.registered ? 'ok' : 'info', result.note)
  }
}

// Switching the choice clears a complaint about the branch you just left.
watch(backendType, () => { harnessError.value = '' })

watch(() => props.authorityEpoch, (epoch, previous) => {
  if (epoch === previous) return
  authorityGeneration += 1
  resetSaveState()
})
watch(() => [props.store, props.authorityEpoch, props.name], () => { slackRequestURL.value = '' })
onBeforeUnmount(() => {
  authorityGeneration += 1
  slackRequestURL.value = ''
})

async function testChannel(name: string): Promise<void> {
  const authority = captureAuthority()
  await mutate(authority.store, {
    run: () => authority.api.testConnection(name),
    success: `Test message sent via ${name}. Check the channel.`,
    failure: `Test of “${name}” failed`,
  })
}

function setDelegate(source: Agent, delegate: string, on: boolean): void {
  const next = on
    ? [...new Set([...(source.spec?.delegates || []), delegate])]
    : (source.spec?.delegates || []).filter(name => name !== delegate)
  saveImmediate('delegates', { delegates: next }, spec => { spec.delegates = next }, 'Delegates saved.', 'delegates')
}

interface OptionalLimitValidation {
  value: number
  error: string
}

function validateOptionalLimit(value: string): OptionalLimitValidation {
  const raw = value.trim()
  if (!raw) return { value: 0, error: '' }
  if (!/^\d+$/.test(raw)) {
    return { value: 0, error: 'Enter a whole number of zero or more, or leave blank for the provider default.' }
  }
  const parsed = Number(raw)
  if (!Number.isSafeInteger(parsed)) return { value: 0, error: 'Enter a whole number within the supported range.' }
  return { value: parsed, error: '' }
}

function setGrants(spec: Agent['spec'], patch: AgentPatch): void {
  spec.tools = spec.tools || {}
  spec.tools.interactive = spec.tools.interactive || {}
  spec.tools.background = spec.tools.background || {}
  if (patch.interactiveToolsets) spec.tools.interactive.toolsets = patch.interactiveToolsets
  if (patch.backgroundToolsets) spec.tools.background.toolsets = patch.backgroundToolsets
  if (patch.interactiveConnections) spec.tools.interactive.connections = patch.interactiveConnections
  if (patch.backgroundConnections) spec.tools.background.connections = patch.backgroundConnections
  if (patch.interactiveFamilies) spec.tools.interactive.families = patch.interactiveFamilies
  if (patch.backgroundFamilies) spec.tools.background.families = patch.backgroundFamilies
}
</script>

<template>
  <div v-if="!agent" class="k-card agents-state agents-state-loading k-loading-reveal" role="status">Loading configuration…</div>
  <template v-else>
    <ResourceSectionCard class="agents-config-sec" heading-id="agent-persona-heading" title="Persona" description="Who this agent is and how it should behave on every run.">
      <label>Display name<input v-model="displayName" class="k-input" /></label>
      <label>Description<input v-model="description" class="k-input" placeholder="What this agent is for — shown to you, not to the model." /></label>
      <label>System prompt<textarea v-model="systemPrompt" class="k-input" rows="6" placeholder="You are a concise assistant that…"></textarea></label>
      <div class="agents-form-actions">
        <button class="k-btn k-btn--primary" type="button" :disabled="saveState.persona.status === 'pending'" :aria-busy="saveState.persona.status === 'pending' ? 'true' : undefined" :aria-describedby="feedbackDescription('persona', personaDirty)" @click="savePersona"><Check :stroke-width="1.75" aria-hidden="true" /> {{ saveState.persona.status === 'pending' ? 'Saving persona…' : 'Save persona' }}</button>
        <ConfigSaveFeedback id="agent-persona-save-feedback" action="persona" :status="feedbackStatus('persona', personaDirty)" :newer-edits="newerEdits('persona')" :error="feedbackError('persona')" />
      </div>
    </ResourceSectionCard>

    <ResourceSectionCard class="agents-config-sec" heading-id="agent-backend-heading" title="Backend" description="Choose where this agent runs its turns.">
      <template #actions><Server :stroke-width="1.75" aria-hidden="true" /></template>
      <fieldset class="agents-cap-fs">
        <legend>Execution backend</legend>
        <div class="agents-radiocards">
          <label v-for="mode in BACKEND_MODES" :key="mode.id" class="agents-radiocard k-checkbox-hit" :class="{ sel: mode.id === backendType }">
            <input v-model="backendType" type="radio" name="backend-type" :value="mode.id" />
            <span class="agents-radiocard-t">{{ mode.label }}</span><span class="agents-radiocard-b">{{ mode.blurb }}</span>
          </label>
        </div>
      </fieldset>

      <template v-if="backendType === 'harness'">
        <div v-if="edgeSlice.error && !edgeSlice.hasSnapshot" class="k-inline-notification k-inline-notification--error" role="alert">
          <span class="k-inline-notification__body"><span class="k-inline-notification__message">Could not load machines. {{ edgeSlice.error }}</span></span>
          <button class="k-inline-notification__action" type="button" :disabled="edgeSlice.loading" @click="store.load('edges')">{{ edgeSlice.loading ? 'Retrying…' : 'Retry' }}</button>
        </div>
        <div v-else-if="!edgeSlice.hasSnapshot" class="agents-state agents-state-loading k-loading-reveal" role="status"><span class="agents-spinner k-spin" aria-hidden="true" /> Loading machines…</div>
        <template v-else>
          <div v-if="edgeSlice.error" class="k-inline-notification k-inline-notification--warning" role="status">
            <span class="k-inline-notification__body"><span class="k-inline-notification__message">Showing the last loaded machines. {{ edgeSlice.error }}</span></span>
            <button class="k-inline-notification__action" type="button" :disabled="edgeSlice.loading" @click="store.load('edges')">{{ edgeSlice.loading ? 'Retrying…' : 'Retry' }}</button>
          </div>
          <div class="agents-grid2">
            <label>
              <span id="agent-harness-edge-label">Machine *</span>
              <FormSelect v-model="harnessEdge" :options="edgeOptions" :required="true" labelledby="agent-harness-edge-label" :invalid="Boolean(harnessError) && !harnessEdge" :describedby="['agent-harness-edge-hint', hostEdges.length === 0 ? 'agent-harness-edge-empty' : '', harnessError && !harnessEdge ? 'agent-backend-error' : ''].filter(Boolean).join(' ')" />
              <span id="agent-harness-edge-hint" class="agents-hint">Linux and macOS machines only — a Kubernetes cluster cannot run a harness process.</span>
              <span v-if="hostEdges.length === 0" id="agent-harness-edge-empty" class="agents-hint">No Linux or macOS machine in this workspace yet.
                <a :href="portalHref('/providers/edges/connect/edge', api.tenant())" target="_blank" rel="noopener noreferrer" class="k-btn k-btn--ghost">Connect a machine (new tab)</a>
                <button type="button" class="k-btn k-btn--ghost" :disabled="edgeSlice.loading" @click="store.load('edges')">{{ edgeSlice.loading ? 'Checking machines…' : 'Check again' }}</button>
                Keep this tab open to retain your changes.</span>
            </label>
            <label>
              <span id="agent-harness-credential-label">Harness credential *</span>
              <div v-if="credentialSlice.error && !credentialSlice.hasSnapshot" id="agent-harness-credential-read-error" class="k-inline-notification k-inline-notification--error" role="alert">
                <span class="k-inline-notification__body"><span class="k-inline-notification__message">Could not load harness identities. {{ credentialSlice.error }}</span></span>
                <button class="k-inline-notification__action" type="button" :disabled="credentialSlice.loading" @click="store.load('credentials')">{{ credentialSlice.loading ? 'Retrying…' : 'Retry' }}</button>
              </div>
              <div v-else-if="!credentialSlice.hasSnapshot" id="agent-harness-credential-loading" class="agents-state agents-state-loading k-loading-reveal" role="status"><span class="agents-spinner k-spin" aria-hidden="true" /> Loading harness identities…</div>
              <template v-else>
                <div v-if="credentialSlice.error" class="k-inline-notification k-inline-notification--warning" role="status">
                  <span class="k-inline-notification__body"><span class="k-inline-notification__message">Showing the last loaded harness identities. {{ credentialSlice.error }}</span></span>
                  <button class="k-inline-notification__action" type="button" :disabled="credentialSlice.loading" @click="store.load('credentials')">{{ credentialSlice.loading ? 'Retrying…' : 'Check again' }}</button>
                </div>
                <FormSelect v-model="harnessCredential" :options="harnessCredentialOptions" :required="true" labelledby="agent-harness-credential-label" :invalid="Boolean(harnessError) && Boolean(harnessEdge) && !harnessCredential" :describedby="['agent-harness-credential-hint', harnessCredentials.length === 0 ? 'agent-harness-credential-empty' : '', harnessError && Boolean(harnessEdge) && !harnessCredential ? 'agent-backend-error' : ''].filter(Boolean).join(' ')" />
                <span v-if="harnessCredentials.length === 0" id="agent-harness-credential-empty" class="agents-hint">{{ credentialSlice.error ? 'No harness identities in the last loaded snapshot.' : 'No harness identities yet.' }}
                  <a :href="portalHref('/providers/agents/#/create/model/harness', api.tenant())" target="_blank" rel="noopener noreferrer" class="k-btn k-btn--ghost">Add harness identity (new tab)</a>
                  <button type="button" class="k-btn k-btn--ghost" :disabled="credentialSlice.loading" @click="store.load('credentials')">{{ credentialSlice.loading ? 'Checking identities…' : 'Check again' }}</button>
                  Keep this tab open to retain your changes.</span>
              </template>
              <span id="agent-harness-credential-hint" class="agents-hint">{{ selectedHarness ? `Runs ${selectedHarness} — decided by this credential’s provider.` : 'A claude-code credential means Claude Code; a codex one means Codex.' }}</span>
            </label>
          </div>
          <div class="agents-grid2">
            <label>
              <span id="agent-harness-model-label">Model</span>
              <input id="agent-harness-model" v-model="harnessModel" class="k-input" placeholder="Harness default" aria-labelledby="agent-harness-model-label" aria-describedby="agent-harness-model-hint" />
              <span id="agent-harness-model-hint" class="agents-hint">Optional — blank leaves the harness’s own default.</span>
            </label>
            <label>
              <span id="agent-harness-workspace-label">Working directory</span>
              <FormSelect v-model="harnessWorkspace" :options="workspaceOptions" labelledby="agent-harness-workspace-label" />
            </label>
          </div>
        </template>
      </template>

      <div v-if="harnessError" id="agent-backend-error" class="agents-fielderr" role="alert">{{ harnessError }}</div>
      <p v-if="backendPolicyBlocker" id="agent-backend-policy-blocker" class="agents-hint agents-warn-inline" role="status">
        {{ backendPolicyBlocker }} Save a supported policy before changing this agent to a coding harness.
      </p>

      <div class="agents-fieldset">
        <span class="agents-fieldset-legend">Saved backend readiness</span>
        <div v-if="!backendCondition" class="agents-hint" role="status">Waiting for the backend readiness check.</div>
        <template v-else-if="backendCondition.status === 'True'">
          <StatusBadge status="Backend ready" tone="success" />
          <span v-if="resolvedHarness" class="agents-hint">Turns will run on {{ resolvedHarness }}.</span>
        </template>
        <template v-else>
          <p class="agents-hint agents-warn-inline" role="status"><Circle :stroke-width="1.75" aria-hidden="true" /> This agent cannot start a turn because its backend is not ready.</p>
          <p v-if="backendCondition.reason" class="agents-hint">Readiness reason: <strong>{{ backendCondition.reason }}</strong></p>
          <p v-if="backendCondition.message" class="agents-hint">Details: {{ backendCondition.message }}</p>
        </template>
      </div>

      <div class="agents-form-actions">
        <button class="k-btn k-btn--primary" type="button" :disabled="saveState.backend.status === 'pending' || Boolean(backendPolicyBlocker) || (backendType === 'harness' && !credentialSlice.hasSnapshot)" :aria-busy="saveState.backend.status === 'pending' ? 'true' : undefined" :aria-describedby="[harnessError ? 'agent-backend-error' : '', backendPolicyBlocker ? 'agent-backend-policy-blocker' : '', backendType === 'harness' && !credentialSlice.hasSnapshot ? (credentialSlice.error ? 'agent-harness-credential-read-error' : 'agent-harness-credential-loading') : '', feedbackDescription('backend', backendDirty) || ''].filter(Boolean).join(' ') || undefined" @click="saveBackend"><Check :stroke-width="1.75" aria-hidden="true" /> {{ saveState.backend.status === 'pending' ? 'Saving backend…' : 'Save backend' }}</button>
        <ConfigSaveFeedback id="agent-backend-save-feedback" action="the backend" :status="feedbackStatus('backend', backendDirty)" :newer-edits="newerEdits('backend')" :error="feedbackError('backend')" />
      </div>
    </ResourceSectionCard>

    <ResourceSectionCard v-if="modelBacked" class="agents-config-sec" heading-id="agent-model-heading" title="Model" description="Which credential this agent reasons with. Fallbacks are tried in order when the primary fails.">
      <div v-if="credentialSlice.error && !credentialSlice.hasSnapshot" class="agents-state agents-state-error" role="alert">
        <span>Could not load model credentials. {{ credentialSlice.error }}</span>
        <button class="k-btn k-btn--ghost secondary" type="button" :disabled="credentialSlice.loading" @click="store.load('credentials')">{{ credentialSlice.loading ? 'Retrying…' : 'Retry' }}</button>
      </div>
      <div v-else-if="!credentialSlice.hasSnapshot" class="agents-state agents-state-loading k-loading-reveal" role="status"><span class="agents-spinner k-spin" aria-hidden="true" /> Loading model credentials…</div>
      <div v-else-if="credentialSlice.error" class="agents-stale" role="status">
        Showing the last loaded credentials. {{ credentialSlice.error }}
        <button class="k-btn k-btn--ghost secondary" type="button" :disabled="credentialSlice.loading" @click="store.load('credentials')">{{ credentialSlice.loading ? 'Retrying…' : 'Retry' }}</button>
      </div>
      <label v-if="credentialSlice.hasSnapshot">
        <span id="agent-model-credential-label">Model credential</span>
        <FormSelect v-model="modelCredential" :options="credentialOptions" labelledby="agent-model-credential-label" :describedby="credentials.length === 0 ? 'agent-model-credential-empty' : undefined" />
        <span v-if="credentials.length === 0" id="agent-model-credential-empty" class="agents-hint">No models yet — <button type="button" class="k-dashboard-action" @click="emit('navigate', { kind: 'menu', menu: 'models' })">add one under Models</button>.</span>
      </label>
      <div v-if="credentialSlice.hasSnapshot" class="agents-fieldset">
        <span id="agent-fallbacks-label" class="agents-fieldset-legend">Fallbacks</span>
        <div v-if="fallbacks.length" class="agents-chiprow">
          <span v-for="(fallback, index) in fallbacks" :key="`${fallback}-${index}`" class="agents-chip">
            {{ fallback }}
            <button class="k-icon-action agents-chip-x" :aria-label="`Remove fallback ${fallback}`" type="button" @click="removeFallback(index)"><X :stroke-width="1.75" aria-hidden="true" /></button>
          </span>
        </div>
        <FormSelect v-if="availableFallbacks.length" class="agents-addselect" :model-value="''" :options="fallbackOptions" labelledby="agent-fallbacks-label" :describedby="fallbacks.length === 0 ? 'agent-fallbacks-empty' : undefined" @update:model-value="addFallback" />
        <span v-if="fallbacks.length === 0" id="agent-fallbacks-empty" class="agents-hint">None — a model failure fails the run.</span>
      </div>
      <div v-if="credentialSlice.hasSnapshot" class="agents-form-actions">
        <button class="k-btn k-btn--primary" type="button" :disabled="saveState.model.status === 'pending'" :aria-busy="saveState.model.status === 'pending' ? 'true' : undefined" :aria-describedby="feedbackDescription('model', modelDirty)" @click="saveModel"><Check :stroke-width="1.75" aria-hidden="true" /> {{ saveState.model.status === 'pending' ? 'Saving model…' : 'Save model' }}</button>
        <ConfigSaveFeedback id="agent-model-save-feedback" action="model" :status="feedbackStatus('model', modelDirty)" :newer-edits="newerEdits('model')" :error="feedbackError('model')" />
      </div>
    </ResourceSectionCard>

    <ResourceSectionCard class="agents-config-sec" heading-id="agent-policy-heading" title="Autonomy &amp; budget" :description="policyDescription">
      <fieldset class="agents-cap-fs">
        <legend>Autonomy</legend>
        <p v-if="harnessPolicyTarget" id="agent-harness-permission-policy" class="agents-hint" role="status">
          Coding harness permission prompts are configured by Claude Code or Codex on the machine. Railgrid cannot gate harness actions; Ask is the only supported Railgrid value for this backend.
        </p>
        <div class="agents-radiocards">
          <label v-for="mode in AUTONOMY_MODES" :key="mode.id" class="agents-radiocard k-checkbox-hit" :class="{ sel: mode.id === autonomy }">
            <input v-model="autonomy" type="radio" name="autonomy" :value="mode.id" :disabled="harnessPolicyTarget && mode.id !== 'ask'" />
            <span class="agents-radiocard-t">{{ mode.label }}</span><span class="agents-radiocard-b">{{ autonomyBlurb(mode.id) }}</span>
          </label>
        </div>
      </fieldset>
      <div class="agents-fieldset">
        <span class="agents-fieldset-legend">Budget</span>
        <div class="agents-grid2">
          <label>Monthly budget (USD)<input v-model="budgetUSD" class="k-input" aria-label="Monthly budget (USD)" inputmode="decimal" placeholder="blank = unlimited" :aria-invalid="budgetUSDError ? 'true' : undefined" :aria-describedby="budgetUSDError ? 'agent-budget-usd-error' : undefined" /><span v-if="budgetUSDError" id="agent-budget-usd-error" class="agents-fielderr" role="alert">{{ budgetUSDError }}</span></label>
          <label>Monthly token cap<input v-model="budgetTokens" class="k-input" aria-label="Monthly token cap" inputmode="numeric" placeholder="blank = unlimited" :aria-invalid="budgetTokensError ? 'true' : undefined" :aria-describedby="budgetTokensError ? 'agent-budget-tokens-error' : undefined" /><span v-if="budgetTokensError" id="agent-budget-tokens-error" class="agents-fielderr" role="alert">{{ budgetTokensError }}</span></label>
        </div>
        <p class="agents-hint">Monthly caps apply to either backend and use usage reported by that backend; harness caps depend on usage reported by the runner.</p>
      </div>
      <div class="agents-fieldset">
        <span class="agents-fieldset-legend">Limits</span>
        <div class="agents-grid2">
          <label v-if="!harnessPolicyTarget"><span id="agent-max-tool-turns-label">Max tool turns</span><input v-model="maxToolTurns" class="k-input" inputmode="numeric" placeholder="blank = provider default" aria-labelledby="agent-max-tool-turns-label" :aria-invalid="maxToolTurnsValidation.error ? 'true' : undefined" :aria-describedby="['agent-max-tool-turns-hint', maxToolTurnsValidation.error ? 'agent-max-tool-turns-error' : ''].filter(Boolean).join(' ')" /><span id="agent-max-tool-turns-hint" class="agents-hint">How many tool-call rounds one model run may take before it stops. Leave blank for the provider default.</span><span v-if="maxToolTurnsValidation.error" id="agent-max-tool-turns-error" class="agents-fielderr" role="alert">{{ maxToolTurnsValidation.error }}</span></label>
          <div v-else class="agents-fieldset" data-harness-tool-turn-limit>
            <span class="agents-fieldset-legend">Model-only limit</span>
            <p v-if="hasToolTurnLimit" class="agents-hint" role="status">This draft still has a {{ maxToolTurns }}-turn cap. A harness owns its tool-call loop, so Railgrid cannot enforce this limit.</p>
            <p v-else class="agents-hint">A harness owns its tool-call loop. Railgrid cannot cap individual tool calls.</p>
            <button v-if="hasToolTurnDraft" class="k-btn k-btn--ghost" type="button" :disabled="saveState.policy.status === 'pending'" @click="maxToolTurns = ''">Clear draft limit</button>
          </div>
          <label><span id="agent-run-timeout-label">Run timeout (seconds)</span><input v-model="timeoutSeconds" class="k-input" inputmode="numeric" placeholder="blank = provider default" aria-labelledby="agent-run-timeout-label" :aria-invalid="timeoutSecondsValidation.error ? 'true' : undefined" :aria-describedby="['agent-run-timeout-hint', timeoutSecondsValidation.error ? 'agent-run-timeout-error' : ''].filter(Boolean).join(' ')" /><span id="agent-run-timeout-hint" class="agents-hint">Railgrid stops the run when this wall-clock limit expires. Leave blank for the provider default.</span><span v-if="timeoutSecondsValidation.error" id="agent-run-timeout-error" class="agents-fielderr" role="alert">{{ timeoutSecondsValidation.error }}</span></label>
        </div>
      </div>
      <p v-if="harnessPolicyIssues.length" id="agent-harness-policy-issues" class="agents-fielderr" role="alert">{{ harnessPolicyIssues.join(' ') }}</p>
      <div class="agents-form-actions">
        <button class="k-btn k-btn--primary" type="button" :disabled="saveState.policy.status === 'pending' || harnessPolicyIssues.length > 0 || policyLimitInvalid" :aria-busy="saveState.policy.status === 'pending' ? 'true' : undefined" :aria-describedby="[harnessPolicyIssues.length ? 'agent-harness-policy-issues' : '', maxToolTurnsValidation.error && !harnessPolicyTarget ? 'agent-max-tool-turns-error' : '', timeoutSecondsValidation.error ? 'agent-run-timeout-error' : '', feedbackDescription('policy', policyDirty) || ''].filter(Boolean).join(' ') || undefined" @click="savePolicy"><Check :stroke-width="1.75" aria-hidden="true" /> {{ saveState.policy.status === 'pending' ? 'Saving policy…' : 'Save policy' }}</button>
        <ConfigSaveFeedback id="agent-policy-save-feedback" action="autonomy, budget and limits" :status="feedbackStatus('policy', policyDirty)" :newer-edits="newerEdits('policy')" :error="feedbackError('policy')" />
      </div>
    </ResourceSectionCard>

    <Teleport to="#agents-workbench-panel-tools" defer :disabled="!workbenchSections">
    <ResourceSectionCard class="agents-config-sec" heading-id="agent-tools-heading" title="Tools &amp; toolsets" description="What this agent can call. Chat always gets a granted tool; background grants also allow it on schedules, triggers, and heartbeats, which run with nobody watching.">
      <template #actions><Wrench :stroke-width="1.75" aria-hidden="true" /></template>
      <!-- A harness brings its own tools, and the API REJECTS spec.tools on a
           harness-backed agent. Showing the grants disabled rather than hiding
           the card is the honest version: an ignored grant reads as a granted
           one, and a card that vanished reads as a feature that went missing. -->
      <p v-if="harnessStored" class="agents-hint agents-warn-inline" data-tools-disabled role="status">
        <Circle :stroke-width="1.75" aria-hidden="true" /> This agent uses the coding harness’s own tools. Agent tool grants do not apply; switch to <strong>Model</strong> above to configure tools here.
      </p>
      <template v-else>
      <ConfigSaveFeedback id="agent-tools-save-feedback" action="tool access" :status="feedbackStatus('tools')" :error="feedbackError('tools')" />
      <fieldset class="agents-wire-fs">
        <legend><Puzzle :stroke-width="1.75" aria-hidden="true" /> Toolsets</legend>
        <div v-if="toolsetSlice.error && !toolsetSlice.hasSnapshot" class="agents-state agents-state-error" role="alert">
          <span>Could not load toolsets. {{ toolsetSlice.error }}</span>
          <button class="k-btn k-btn--ghost secondary" type="button" :disabled="toolsetSlice.loading" @click="store.load('toolsets')">{{ toolsetSlice.loading ? 'Retrying…' : 'Retry' }}</button>
        </div>
        <div v-else-if="!toolsetSlice.hasSnapshot" class="agents-state agents-state-loading k-loading-reveal" role="status"><span class="agents-spinner k-spin" aria-hidden="true" /> Loading toolsets…</div>
        <template v-else>
          <div v-if="toolsetSlice.error" class="agents-stale" role="status">
            Showing the last loaded toolsets. {{ toolsetSlice.error }}
            <button class="k-btn k-btn--ghost secondary" type="button" :disabled="toolsetSlice.loading" @click="store.load('toolsets')">{{ toolsetSlice.loading ? 'Retrying…' : 'Retry' }}</button>
          </div>
          <div v-for="toolset in toolsets" :key="toolset.metadata.name" class="agents-tool-row">
            <label class="agents-check k-checkbox-hit"><input type="checkbox" :checked="linkedToolsets(agent).has(toolset.metadata.name)" @change="setToolsetLinked(agent, toolset.metadata.name, ($event.target as HTMLInputElement).checked)" /> {{ toolset.spec.displayName || toolset.metadata.name }}</label>
            <label class="agents-check agents-bg-toggle k-checkbox-hit" title="Background runs have no human watching, so tools stay interactive-only unless opted in here."><input type="checkbox" :checked="backgroundToolsets(agent).has(toolset.metadata.name)" :disabled="!linkedToolsets(agent).has(toolset.metadata.name)" @change="setToolsetBackground(agent, toolset.metadata.name, ($event.target as HTMLInputElement).checked)" /><Clock :stroke-width="1.75" aria-hidden="true" /> background</label>
          </div>
          <p v-if="toolsets.length === 0" class="agents-hint">No toolsets yet — create one in the <button type="button" class="k-dashboard-action" @click="emit('navigate', { kind: 'menu', menu: 'connections' })">Connections</button> tab.</p>
        </template>
      </fieldset>

      <fieldset class="agents-wire-fs">
        <legend><Globe2 :stroke-width="1.75" aria-hidden="true" /> Built-in capabilities</legend>
        <p class="muted">Tools the agent has on its own, with nothing to wire up. Reading the web needs no connection; <strong>searching</strong> it needs a websearch tool granted below, and without one the agent can only read pages it is given a link to. Turning on fan-out also teaches the agent how to use it — you do not need to write that into the prompt.</p>
        <div class="agents-tool-row">
          <label class="agents-check k-checkbox-hit"><input type="checkbox" :checked="familyEnabled(agent, 'web')" @change="setFamily(agent, 'web', 'Web access', ($event.target as HTMLInputElement).checked, false)" /><span class="agents-check-copy"><span>Read the web</span><span class="muted">web_fetch{{ familyEnabled(agent, 'web') && !hasSearchTool(agent) ? ' — no search tool wired' : '' }}</span></span></label>
          <label class="agents-check agents-bg-toggle k-checkbox-hit"><input type="checkbox" :checked="familyEnabled(agent, 'web', true)" :disabled="!familyEnabled(agent, 'web')" @change="setFamily(agent, 'web', 'Web access', ($event.target as HTMLInputElement).checked, true)" /><Clock :stroke-width="1.75" aria-hidden="true" /> background</label>
        </div>
        <div class="agents-tool-row">
          <label class="agents-check k-checkbox-hit"><input type="checkbox" :checked="familyEnabled(agent, 'spawn')" @change="setFamily(agent, 'spawn', 'Research fan-out', ($event.target as HTMLInputElement).checked, false)" /><span class="agents-check-copy"><span>Research fan-out</span><span class="muted">spawn + join{{ familyEnabled(agent, 'spawn') && !familyEnabled(agent, 'web') ? ' — workers will have no web access' : '' }}</span></span></label>
          <label class="agents-check agents-bg-toggle k-checkbox-hit"><input type="checkbox" :checked="familyEnabled(agent, 'spawn', true)" :disabled="!familyEnabled(agent, 'spawn')" @change="setFamily(agent, 'spawn', 'Research fan-out', ($event.target as HTMLInputElement).checked, true)" /><Clock :stroke-width="1.75" aria-hidden="true" /> background</label>
        </div>
        <div class="agents-tool-row">
          <label class="agents-check k-checkbox-hit"><input type="checkbox" :checked="familyEnabled(agent, 'visualization')" @change="setFamily(agent, 'visualization', 'Visualize data', ($event.target as HTMLInputElement).checked, false)" /><span class="agents-check-copy"><span>Visualize data</span><span class="muted">Create charts in chat from supplied data</span></span></label>
          <label class="agents-check agents-bg-toggle k-checkbox-hit"><input type="checkbox" :checked="familyEnabled(agent, 'visualization', true)" :disabled="!familyEnabled(agent, 'visualization')" @change="setFamily(agent, 'visualization', 'Visualize data', ($event.target as HTMLInputElement).checked, true)" /><Clock :stroke-width="1.75" aria-hidden="true" /> background</label>
        </div>
        <p v-if="familyEnabled(agent, 'spawn') && !familyEnabled(agent, 'web')" class="agents-hint agents-warn-inline"><Circle :stroke-width="1.75" aria-hidden="true" /> This agent can spawn workers but has no web access, so a worker inherits none either — a fan-out would answer from the model alone. Turn on <strong>Read the web</strong>, and wire a websearch tool for real searching.</p>
      </fieldset>

      <fieldset class="agents-wire-fs">
        <legend><Wrench :stroke-width="1.75" aria-hidden="true" /> Direct tools</legend>
        <div v-if="connectionSlice.error && !connectionSlice.hasSnapshot" class="agents-state agents-state-error" role="alert">
          <span>Could not load tool connections. {{ connectionSlice.error }}</span>
          <button class="k-btn k-btn--ghost secondary" type="button" :disabled="connectionSlice.loading" @click="store.load('connections')">{{ connectionSlice.loading ? 'Retrying…' : 'Retry' }}</button>
        </div>
        <div v-else-if="!connectionSlice.hasSnapshot" class="agents-state agents-state-loading k-loading-reveal" role="status"><span class="agents-spinner k-spin" aria-hidden="true" /> Loading tool connections…</div>
        <template v-else>
          <div v-if="connectionSlice.error" class="agents-stale" role="status">
            Showing the last loaded connections. {{ connectionSlice.error }}
            <button class="k-btn k-btn--ghost secondary" type="button" :disabled="connectionSlice.loading" @click="store.load('connections')">{{ connectionSlice.loading ? 'Retrying…' : 'Retry' }}</button>
          </div>
          <div v-for="connection in toolConnections" :key="connection.metadata.name" class="agents-tool-row">
            <label class="agents-check k-checkbox-hit"><input type="checkbox" :checked="linkedTools(agent).has(connection.metadata.name)" @change="setToolLinked(agent, connection.metadata.name, ($event.target as HTMLInputElement).checked)" /><span class="agents-check-copy"><span>{{ connection.spec.displayName || connection.metadata.name }}</span><span class="muted">{{ connection.spec.type }}</span></span></label>
            <label class="agents-check agents-bg-toggle k-checkbox-hit"><input type="checkbox" :checked="backgroundTools(agent).has(connection.metadata.name)" :disabled="!linkedTools(agent).has(connection.metadata.name)" @change="setToolBackground(agent, connection.metadata.name, ($event.target as HTMLInputElement).checked)" /><Clock :stroke-width="1.75" aria-hidden="true" /> background</label>
          </div>
          <p v-if="toolConnections.length === 0" class="agents-hint">No tools yet — add a GitHub / MCP / web-search connection under <button type="button" class="k-dashboard-action" @click="emit('navigate', { kind: 'menu', menu: 'connections' })">Connections</button>.</p>
        </template>
      </fieldset>
      </template>
    </ResourceSectionCard>
    </Teleport>

    <ResourceSectionCard class="agents-config-sec" heading-id="agent-channels-heading" title="Channels" description="Where this agent messages you — and, for chat channels, where you message it. Bind a primary channel plus named secondaries; schedules and triggers can route to any of them by name.">
      <SecretHandoff v-if="slackRequestURL" :value="slackRequestURL" label="Slack request URL" copy-label="Copy Slack request URL" @cleared="slackRequestURL = ''" />
      <div v-if="connectionSlice.error && !connectionSlice.hasSnapshot" class="agents-state agents-state-error" role="alert">
        <span>Could not load channel connections. {{ connectionSlice.error }}</span>
        <button class="k-btn k-btn--ghost secondary" type="button" :disabled="connectionSlice.loading" @click="store.load('connections')">{{ connectionSlice.loading ? 'Retrying…' : 'Retry' }}</button>
      </div>
      <div v-else-if="!connectionSlice.hasSnapshot" class="agents-state agents-state-loading k-loading-reveal" role="status"><span class="agents-spinner k-spin" aria-hidden="true" /> Loading channel connections…</div>
      <div v-else-if="connectionSlice.error" class="agents-stale" role="status">
        Showing the last loaded connections. {{ connectionSlice.error }}
        <button class="k-btn k-btn--ghost secondary" type="button" :disabled="connectionSlice.loading" @click="store.load('connections')">{{ connectionSlice.loading ? 'Retrying…' : 'Retry' }}</button>
      </div>
      <p v-if="connectionSlice.hasSnapshot && channelConnections.length === 0" class="agents-hint">No channels yet — add a Telegram / Slack / Discord / email connection under <button type="button" class="k-dashboard-action" @click="emit('navigate', { kind: 'menu', menu: 'connections' })">Connections</button>.</p>
      <div v-if="connectionSlice.hasSnapshot" class="agents-chan-editor" role="group" aria-labelledby="agent-channels-heading" :aria-describedby="channelError ? 'agent-channels-error' : undefined">
        <div v-for="row in channels" :key="row.key" class="agents-chan-row">
          <input class="k-input agents-chan-name" placeholder="primary" aria-label="Channel role name" :value="row.name || ''" :aria-invalid="channelErrorTarget?.key === row.key && channelErrorTarget.field === 'name' ? 'true' : undefined" :aria-describedby="channelErrorTarget?.key === row.key && channelErrorTarget.field === 'name' ? 'agent-channels-error' : undefined" @input="patchChannelRow(row.key, { name: ($event.target as HTMLInputElement).value })" />
          <FormSelect class="agents-chan-conn" :model-value="row.connectionRef" :options="channelOptions" :labelledby="`agent-channel-${row.key}-label`" :invalid="channelErrorTarget?.key === row.key && channelErrorTarget.field === 'connection'" :describedby="channelErrorTarget?.key === row.key && channelErrorTarget.field === 'connection' ? 'agent-channels-error' : undefined" @update:model-value="patchChannelRow(row.key, { connectionRef: $event })" />
          <span :id="`agent-channel-${row.key}-label`" class="sr-only">Channel connection</span>
          <label class="agents-chan-primary k-checkbox-hit" title="Default channel for output with no channel set"><input type="radio" name="chan-primary" :checked="Boolean(row.primary)" @change="setPrimaryChannel(row.key)" /> primary</label>
          <button class="k-icon-action agents-iconbtn-danger" type="button" :aria-label="`Remove channel ${row.name || ''}`" title="Remove channel" @click="removeChannel(row.key)"><Trash2 :stroke-width="1.75" aria-hidden="true" /></button>
        </div>
      </div>
      <div v-if="connectionSlice.hasSnapshot && channelError" id="agent-channels-error" class="agents-fielderr" role="alert">{{ channelError }}</div>
      <div v-if="connectionSlice.hasSnapshot" class="agents-form-actions">
        <button class="k-btn k-btn--ghost secondary" type="button" :disabled="channelConnections.length === 0" @click="addChannel"><Plus :stroke-width="1.75" aria-hidden="true" /> Add channel</button>
        <button class="k-btn k-btn--primary" type="button" :disabled="saveState.channels.status === 'pending'" :aria-busy="saveState.channels.status === 'pending' ? 'true' : undefined" :aria-describedby="[channelError ? 'agent-channels-error' : '', feedbackDescription('channels', channelsDirty) || ''].filter(Boolean).join(' ') || undefined" @click="saveChannels"><Check :stroke-width="1.75" aria-hidden="true" /> {{ saveState.channels.status === 'pending' ? 'Saving channels…' : 'Save channels' }}</button>
        <ConfigSaveFeedback id="agent-channels-save-feedback" action="channels" :status="feedbackStatus('channels', channelsDirty)" :newer-edits="newerEdits('channels')" :error="feedbackError('channels')" />
      </div>
      <div v-if="connectionSlice.hasSnapshot && agent.spec?.channels?.length" class="agents-chan-inbound">
        <div v-for="bound in agent.spec.channels" :key="`${bound.name}-${bound.connectionRef}`" class="agents-inbound-line">
          <template v-if="inboundState(bound.connectionRef).found">
            <span class="k-badge agents-badge" :class="{ 'agents-cat-channel': inboundState(bound.connectionRef).on }">
              {{ bound.name }}<Star v-if="bound.primary" :stroke-width="1.75" aria-label="primary" /> · <ArrowLeftRight :stroke-width="1.75" aria-hidden="true" /> inbound {{ inboundState(bound.connectionRef).on ? 'on' : 'off' }}
            </span>
            <span class="muted">{{ inboundState(bound.connectionRef).note }}</span>
            <span class="agents-inbound-actions">
              <button v-if="inboundState(bound.connectionRef).canEnable" class="k-btn k-btn--ghost secondary" type="button" @click="enableInbound(bound.connectionRef)">Enable inbound</button>
              <button class="k-btn k-btn--ghost secondary" type="button" @click="testChannel(bound.connectionRef)"><Send :stroke-width="1.75" aria-hidden="true" /> Test</button>
            </span>
          </template>
          <template v-else>
            <span class="k-badge agents-badge">{{ bound.name }}</span><span class="muted">Connection “{{ bound.connectionRef }}” not found — pick one above and save.</span>
          </template>
        </div>
      </div>
    </ResourceSectionCard>

    <Teleport to="#agents-workbench-panel-automation" defer :disabled="!workbenchSections">
      <Automation :store="store" :api="api" kind="schedule" :agent="name" :authority-epoch="authorityEpoch" @navigate="emit('navigate', $event)" />
      <Automation :store="store" :api="api" kind="trigger" :agent="name" :authority-epoch="authorityEpoch" @navigate="emit('navigate', $event)" />
    </Teleport>

    <ResourceSectionCard v-if="otherAgents.length" class="agents-config-sec" heading-id="agent-delegates-heading" title="Delegates" description="Agents this one may hand work to. A delegated run bills against this agent’s budget.">
      <div class="agents-checkrow">
        <label v-for="other in otherAgents" :key="other.metadata.name" class="agents-check k-checkbox-hit"><input type="checkbox" :checked="(agent.spec?.delegates || []).includes(other.metadata.name)" @change="setDelegate(agent, other.metadata.name, ($event.target as HTMLInputElement).checked)" /> {{ other.spec?.displayName || other.metadata.name }}</label>
      </div>
      <ConfigSaveFeedback id="agent-delegates-save-feedback" action="delegates" :status="feedbackStatus('delegates')" :error="feedbackError('delegates')" />
    </ResourceSectionCard>

    <slot name="destructive-actions" />
  </template>
</template>
