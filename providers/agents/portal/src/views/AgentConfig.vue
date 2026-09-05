<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch, watchEffect } from 'vue'
import {
  ArrowLeftRight,
  Check,
  Circle,
  Clock,
  Globe2,
  Plus,
  Puzzle,
  Send,
  Star,
  Trash2,
  Wrench,
  X,
} from 'lucide-vue-next'
import type { ApiClient } from '../api'
import { validateBudgetInputs } from '../budget-validation'
import SecretHandoff from '../components/SecretHandoff.vue'
import { channelInbound } from '../conn-defs'
import { mutate } from '../mutate'
import FormSelect, { type FormSelectOption } from '../portalkit/FormSelect.vue'
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue'
import { toast } from '../portalkit/toast'
import type { Route } from '../router'
import type { AppStore } from '../store'
import type { Agent, AgentChannel, AgentPatch, Autonomy } from '../types'
import { queueAgentConfigSave } from '../vue/config-save-queue'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'
import Automation from './Automation.vue'

const AUTONOMY_MODES: { id: Autonomy; label: string; blurb: string }[] = [
  { id: 'suggest', label: 'Suggest', blurb: 'Every tool call waits for your approval. Safest; most interruptions.' },
  { id: 'ask', label: 'Ask', blurb: 'Only tools matched by a grant’s approval patterns wait for you.' },
  { id: 'auto', label: 'Auto', blurb: 'Tools run without asking. Use only with tools you trust unattended.' },
]

interface ChannelRow extends AgentChannel { key: number }

const props = withDefaults(defineProps<{
  store: AppStore
  api: ApiClient
  name: string
  authorityEpoch?: number
}>(), { authorityEpoch: 0 })
const emit = defineEmits<{ navigate: [route: Route] }>()
const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)

const displayName = ref('')
const description = ref('')
const systemPrompt = ref('')
const modelCredential = ref('')
const fallbacks = ref<string[]>([])
const autonomy = ref<Autonomy>('ask')
const budgetUSD = ref('')
const budgetTokens = ref('')
const budgetUSDError = ref('')
const budgetTokensError = ref('')
const maxToolTurns = ref('')
const timeoutSeconds = ref('')
const channels = ref<ChannelRow[]>([])
const channelError = ref('')
const channelErrorTarget = ref<{ key: number; field: 'name' | 'connection' } | null>(null)
const slackRequestURL = ref('')

let hydratedStore: AppStore | null = null
let hydratedName = ''
let rowKey = 0

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
const credentials = computed(() => credentialSlice.value.data)
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

function hydrate(source: Agent): void {
  displayName.value = source.spec?.displayName || source.metadata.name
  description.value = source.spec?.description || ''
  systemPrompt.value = source.spec?.systemPrompt || ''
  modelCredential.value = source.spec?.models?.chat || ''
  fallbacks.value = [...(source.spec?.modelFallbacks || [])]
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
}

// Store refreshes invalidate the computed values but never overwrite a draft.
// A new store authority or agent identity is a new form and hydrates once.
watchEffect(() => {
  const source = agent.value
  if (!source || (hydratedStore === props.store && hydratedName === props.name)) return
  hydrate(source)
  hydratedStore = props.store
  hydratedName = props.name
})

function save(patch: AgentPatch, apply: (spec: Agent['spec']) => void, success: string): Promise<boolean> {
  return queueAgentConfigSave({
    store: props.store,
    api: props.api,
    name: props.name,
    patch,
    apply,
    success,
  })
}

function savePersona(): void {
  const nextName = displayName.value.trim()
  const nextDescription = description.value.trim()
  const nextPrompt = systemPrompt.value
  void save(
    { displayName: nextName, description: nextDescription, systemPrompt: nextPrompt },
    spec => {
      spec.displayName = nextName
      spec.description = nextDescription
      spec.systemPrompt = nextPrompt
    },
    'Persona saved.',
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
  const credential = modelCredential.value
  const nextFallbacks = [...fallbacks.value]
  void save(
    { modelCredential: credential, modelFallbacks: nextFallbacks },
    spec => {
      spec.models = { ...(spec.models || {}), chat: credential }
      spec.modelFallbacks = nextFallbacks
    },
    'Model saved.',
  )
}

function savePolicy(): void {
  const budget = validateBudgetInputs(budgetUSD.value, budgetTokens.value)
  budgetUSDError.value = budget.usdError
  budgetTokensError.value = budget.tokenError
  if (budget.usdError || budget.tokenError) return
  const tokens = budget.budgetTokens
  const turns = intOrZero(maxToolTurns.value)
  const timeout = intOrZero(timeoutSeconds.value)
  const usd = budget.budgetUSD
  const mode = autonomy.value
  void save(
    { autonomy: mode, budgetUSD: usd, budgetTokens: tokens, maxToolTurns: turns, timeoutSeconds: timeout },
    spec => {
      spec.autonomy = mode
      spec.budget = { ...(spec.budget || {}), usdLimit: usd, tokenLimit: tokens }
      spec.limits = { maxToolTurns: turns, timeoutSeconds: timeout }
    },
    'Autonomy, budget and limits saved.',
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

function setToolsetLinked(source: Agent, toolset: string, on: boolean): void {
  const interactive = source.spec?.tools?.interactive?.toolsets || []
  const background = source.spec?.tools?.background?.toolsets || []
  const nextInteractive = on ? [...new Set([...interactive, toolset])] : interactive.filter(name => name !== toolset)
  const nextBackground = on ? background : background.filter(name => name !== toolset)
  void save(
    { interactiveToolsets: nextInteractive, backgroundToolsets: nextBackground },
    spec => setGrants(spec, { interactiveToolsets: nextInteractive, backgroundToolsets: nextBackground }),
    on ? 'Toolset linked.' : 'Toolset unlinked.',
  )
}

function setToolsetBackground(source: Agent, toolset: string, on: boolean): void {
  const background = source.spec?.tools?.background?.toolsets || []
  const next = on ? [...new Set([...background, toolset])] : background.filter(name => name !== toolset)
  void save(
    { backgroundToolsets: next },
    spec => setGrants(spec, { backgroundToolsets: next }),
    on ? 'Toolset enabled for background runs.' : 'Toolset is now interactive-only.',
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
  void save(patch, spec => setGrants(spec, patch), on ? 'Tool granted.' : 'Tool removed.')
}

function setToolBackground(source: Agent, connection: string, on: boolean): void {
  const background = source.spec?.tools?.background?.connections || []
  const next = on ? [...new Set([...background, connection])] : background.filter(name => name !== connection)
  const patch: AgentPatch = {
    backgroundConnections: next,
    backgroundFamilies: props.store.familiesFor(next, source.spec?.tools?.background?.families),
  }
  void save(
    patch,
    spec => setGrants(spec, patch),
    on ? 'Tool enabled for background runs.' : 'Tool is now interactive-only.',
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
  void save(patch, spec => setGrants(spec, patch), message)
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
  await save({ channels: payload }, spec => { spec.channels = payload }, 'Channels saved.')
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

watch(() => [props.store, props.authorityEpoch, props.name], () => { slackRequestURL.value = '' })
onBeforeUnmount(() => { slackRequestURL.value = '' })

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
  void save({ delegates: next }, spec => { spec.delegates = next }, 'Delegates saved.')
}

function intOrZero(value: string): number {
  const parsed = Number(value.trim())
  return Number.isFinite(parsed) && parsed > 0 ? Math.floor(parsed) : 0
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
      <div class="agents-form-actions"><button class="k-btn k-btn--primary" type="button" @click="savePersona"><Check :stroke-width="1.75" aria-hidden="true" /> Save persona</button></div>
    </ResourceSectionCard>

    <ResourceSectionCard class="agents-config-sec" heading-id="agent-model-heading" title="Model" description="Which credential this agent reasons with. Fallbacks are tried in order when the primary fails.">
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
        <FormSelect v-model="modelCredential" :options="credentialOptions" labelledby="agent-model-credential-label" />
        <span v-if="credentials.length === 0" class="agents-hint">No models yet — <button type="button" class="k-dashboard-action" @click="emit('navigate', { kind: 'menu', menu: 'models' })">add one under Models</button>.</span>
      </label>
      <div v-if="credentialSlice.hasSnapshot" class="agents-fieldset">
        <span id="agent-fallbacks-label" class="agents-fieldset-legend">Fallbacks</span>
        <div v-if="fallbacks.length" class="agents-chiprow">
          <span v-for="(fallback, index) in fallbacks" :key="`${fallback}-${index}`" class="agents-chip">
            {{ fallback }}
            <button class="k-icon-action agents-chip-x" :aria-label="`Remove fallback ${fallback}`" type="button" @click="removeFallback(index)"><X :stroke-width="1.75" aria-hidden="true" /></button>
          </span>
        </div>
        <span v-else class="agents-hint">None — a model failure fails the run.</span>
        <FormSelect v-if="availableFallbacks.length" class="agents-addselect" :model-value="''" :options="fallbackOptions" labelledby="agent-fallbacks-label" @update:model-value="addFallback" />
      </div>
      <div v-if="credentialSlice.hasSnapshot" class="agents-form-actions">
        <button class="k-btn k-btn--primary" type="button" @click="saveModel"><Check :stroke-width="1.75" aria-hidden="true" /> Save model</button>
        <span v-if="agent.spec?.models?.chat !== modelCredential" class="agents-hint">unsaved change</span>
      </div>
    </ResourceSectionCard>

    <ResourceSectionCard class="agents-config-sec" heading-id="agent-policy-heading" title="Autonomy &amp; budget" description="Autonomy decides which tool calls stop and wait for you. It is enforced on every run — a paused run shows up in Activity as PendingApproval.">
      <div class="agents-radiocards">
        <label v-for="mode in AUTONOMY_MODES" :key="mode.id" class="agents-radiocard" :class="{ sel: mode.id === autonomy }">
          <input v-model="autonomy" type="radio" name="autonomy" :value="mode.id" />
          <span class="agents-radiocard-t">{{ mode.label }}</span><span class="agents-radiocard-b">{{ mode.blurb }}</span>
        </label>
      </div>
      <div class="agents-fieldset">
        <span class="agents-fieldset-legend">Budget</span>
        <div class="agents-grid2">
          <label>Monthly budget (USD)<input v-model="budgetUSD" class="k-input" inputmode="decimal" placeholder="blank = unlimited" :aria-invalid="budgetUSDError ? 'true' : undefined" :aria-describedby="budgetUSDError ? 'agent-budget-usd-error' : undefined" /><span v-if="budgetUSDError" id="agent-budget-usd-error" class="agents-fielderr" role="alert">{{ budgetUSDError }}</span></label>
          <label>Monthly token cap<input v-model="budgetTokens" class="k-input" inputmode="numeric" placeholder="blank = unlimited" :aria-invalid="budgetTokensError ? 'true' : undefined" :aria-describedby="budgetTokensError ? 'agent-budget-tokens-error' : undefined" /><span v-if="budgetTokensError" id="agent-budget-tokens-error" class="agents-fielderr" role="alert">{{ budgetTokensError }}</span></label>
        </div>
      </div>
      <div class="agents-fieldset">
        <span class="agents-fieldset-legend">Limits</span>
        <div class="agents-grid2">
          <label>Max tool turns<input v-model="maxToolTurns" class="k-input" inputmode="numeric" placeholder="blank = provider default" /><span class="agents-hint">How many tool-call rounds one run may take before it stops.</span></label>
          <label>Run timeout (seconds)<input v-model="timeoutSeconds" class="k-input" inputmode="numeric" placeholder="blank = provider default" /><span class="agents-hint">Wall-clock bound on a run — it is aborted when this elapses.</span></label>
        </div>
      </div>
      <div class="agents-form-actions"><button class="k-btn k-btn--primary" type="button" @click="savePolicy"><Check :stroke-width="1.75" aria-hidden="true" /> Save policy</button></div>
    </ResourceSectionCard>

    <ResourceSectionCard class="agents-config-sec" heading-id="agent-tools-heading" title="Tools &amp; toolsets" description="What this agent can call. Chat always gets a granted tool; background grants also allow it on schedules, triggers, and heartbeats, which run with nobody watching.">
      <template #actions><Wrench :stroke-width="1.75" aria-hidden="true" /></template>
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
            <label class="agents-check"><input type="checkbox" :checked="linkedToolsets(agent).has(toolset.metadata.name)" @change="setToolsetLinked(agent, toolset.metadata.name, ($event.target as HTMLInputElement).checked)" /> {{ toolset.spec.displayName || toolset.metadata.name }}</label>
            <label class="agents-check agents-bg-toggle" title="Background runs have no human watching, so tools stay interactive-only unless opted in here."><input type="checkbox" :checked="backgroundToolsets(agent).has(toolset.metadata.name)" :disabled="!linkedToolsets(agent).has(toolset.metadata.name)" @change="setToolsetBackground(agent, toolset.metadata.name, ($event.target as HTMLInputElement).checked)" /><Clock :stroke-width="1.75" aria-hidden="true" /> background</label>
          </div>
          <p v-if="toolsets.length === 0" class="agents-hint">No toolsets yet — create one in the <button type="button" class="k-dashboard-action" @click="emit('navigate', { kind: 'menu', menu: 'connections' })">Connections</button> tab.</p>
        </template>
      </fieldset>

      <fieldset class="agents-wire-fs">
        <legend><Globe2 :stroke-width="1.75" aria-hidden="true" /> Built-in capabilities</legend>
        <p class="muted">Tools the agent has on its own, with nothing to wire up. Reading the web needs no connection; <strong>searching</strong> it needs a websearch tool granted below, and without one the agent can only read pages it is given a link to. Turning on fan-out also teaches the agent how to use it — you do not need to write that into the prompt.</p>
        <div class="agents-tool-row">
          <label class="agents-check"><input type="checkbox" :checked="familyEnabled(agent, 'web')" @change="setFamily(agent, 'web', 'Web access', ($event.target as HTMLInputElement).checked, false)" /> Read the web <span class="muted">web_fetch{{ familyEnabled(agent, 'web') && !hasSearchTool(agent) ? ' — no search tool wired' : '' }}</span></label>
          <label class="agents-check agents-bg-toggle"><input type="checkbox" :checked="familyEnabled(agent, 'web', true)" :disabled="!familyEnabled(agent, 'web')" @change="setFamily(agent, 'web', 'Web access', ($event.target as HTMLInputElement).checked, true)" /><Clock :stroke-width="1.75" aria-hidden="true" /> background</label>
        </div>
        <div class="agents-tool-row">
          <label class="agents-check"><input type="checkbox" :checked="familyEnabled(agent, 'spawn')" @change="setFamily(agent, 'spawn', 'Research fan-out', ($event.target as HTMLInputElement).checked, false)" /> Research fan-out <span class="muted">spawn + join{{ familyEnabled(agent, 'spawn') && !familyEnabled(agent, 'web') ? ' — workers will have no web access' : '' }}</span></label>
          <label class="agents-check agents-bg-toggle"><input type="checkbox" :checked="familyEnabled(agent, 'spawn', true)" :disabled="!familyEnabled(agent, 'spawn')" @change="setFamily(agent, 'spawn', 'Research fan-out', ($event.target as HTMLInputElement).checked, true)" /><Clock :stroke-width="1.75" aria-hidden="true" /> background</label>
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
            <label class="agents-check"><input type="checkbox" :checked="linkedTools(agent).has(connection.metadata.name)" @change="setToolLinked(agent, connection.metadata.name, ($event.target as HTMLInputElement).checked)" /> {{ connection.spec.displayName || connection.metadata.name }} <span class="muted">{{ connection.spec.type }}</span></label>
            <label class="agents-check agents-bg-toggle"><input type="checkbox" :checked="backgroundTools(agent).has(connection.metadata.name)" :disabled="!linkedTools(agent).has(connection.metadata.name)" @change="setToolBackground(agent, connection.metadata.name, ($event.target as HTMLInputElement).checked)" /><Clock :stroke-width="1.75" aria-hidden="true" /> background</label>
          </div>
          <p v-if="toolConnections.length === 0" class="agents-hint">No tools yet — add a GitHub / MCP / web-search connection under <button type="button" class="k-dashboard-action" @click="emit('navigate', { kind: 'menu', menu: 'connections' })">Connections</button>.</p>
        </template>
      </fieldset>
    </ResourceSectionCard>

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
          <label class="agents-chan-primary" title="Default channel for output with no channel set"><input type="radio" name="chan-primary" :checked="Boolean(row.primary)" @change="setPrimaryChannel(row.key)" /> primary</label>
          <button class="k-icon-action agents-iconbtn-danger" type="button" :aria-label="`Remove channel ${row.name || ''}`" title="Remove channel" @click="removeChannel(row.key)"><Trash2 :stroke-width="1.75" aria-hidden="true" /></button>
        </div>
      </div>
      <div v-if="connectionSlice.hasSnapshot && channelError" id="agent-channels-error" class="agents-fielderr" role="alert">{{ channelError }}</div>
      <div v-if="connectionSlice.hasSnapshot" class="agents-form-actions">
        <button class="k-btn k-btn--ghost secondary" type="button" :disabled="channelConnections.length === 0" @click="addChannel"><Plus :stroke-width="1.75" aria-hidden="true" /> Add channel</button>
        <button class="k-btn k-btn--primary" type="button" :aria-describedby="channelError ? 'agent-channels-error' : undefined" @click="saveChannels"><Check :stroke-width="1.75" aria-hidden="true" /> Save channels</button>
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

    <Automation :store="store" :api="api" kind="schedule" :agent="name" :authority-epoch="authorityEpoch" @navigate="emit('navigate', $event)" />
    <Automation :store="store" :api="api" kind="trigger" :agent="name" :authority-epoch="authorityEpoch" @navigate="emit('navigate', $event)" />

    <ResourceSectionCard v-if="otherAgents.length" class="agents-config-sec" heading-id="agent-delegates-heading" title="Delegates" description="Agents this one may hand work to. A delegated run bills against this agent’s budget.">
      <div class="agents-checkrow">
        <label v-for="other in otherAgents" :key="other.metadata.name" class="agents-check"><input type="checkbox" :checked="(agent.spec?.delegates || []).includes(other.metadata.name)" @change="setDelegate(agent, other.metadata.name, ($event.target as HTMLInputElement).checked)" /> {{ other.spec?.displayName || other.metadata.name }}</label>
      </div>
    </ResourceSectionCard>
  </template>
</template>
