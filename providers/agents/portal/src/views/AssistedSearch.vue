<script setup lang="ts">
import { ArrowLeft, Sparkles, X } from 'lucide-vue-next'
import { computed, ref } from 'vue'
import type { ApiClient } from '../api'
import {
  DNS_LABEL_RE,
  INSTANCE_SIZES,
  assistDismissed,
  rememberAssistDismissed,
  searxngSetupPrompt,
  selfHostedSearchConfigured,
  type InstanceSize,
} from '../assisted-setup'
import { familiesForConns } from '../conn-defs'
import { mutate } from '../mutate'
import { readTenant } from '../portalkit/tenant'
import FormSelect from '../portalkit/FormSelect.vue'
import type { CreateSuccessDetail, Route } from '../router'
import type { AppStore } from '../store'
import type { ConnectionWrite } from '../types'
import { useAuthorityGuard, useStoreRevision, type AuthoritySnapshot } from '../vue/runtime'

interface Fence { store: AppStore; authorityEpoch?: number; createSession?: number }
const props = withDefaults(defineProps<{ store: AppStore; api: ApiClient; page?: boolean; authorityEpoch?: number; createSession?: number }>(), { page: false })
const emit = defineEmits<{
  navigate: [route: Route]
  'create-success': [detail: CreateSuccessDetail & Fence]
  'create-cancel': [detail: Fence]
}>()

const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)
const agent = ref('')
const connName = ref('search')
const instance = ref('')
const size = ref<InstanceSize>('small')
const errors = ref<Record<string, string>>({})
const busy = ref(false)
const instanceTouched = ref(false)
const dismissed = ref(assistDismissed(readTenant().workspaceUUID))

const agents = computed(() => { revision.value; return props.store.agents.data })
const selectedAgent = computed(() => agent.value || agents.value[0]?.metadata.name || '')
const instanceName = computed(() => (instanceTouched.value ? instance.value : connName.value).trim())
const agentOptions = computed(() => agents.value.map(item => ({
  value: item.metadata.name,
  label: item.spec?.displayName || item.metadata.name,
})))
const infrastructureEnabled = computed(() => { revision.value; return props.store.hasProvider('infrastructure') })
const connectionSlice = computed(() => { revision.value; return { ...props.store.connections } })
const configured = computed(() => selfHostedSearchConfigured(connectionSlice.value.data))
const visibleCard = computed(() => {
  return infrastructureEnabled.value && agents.value.length > 0
    && connectionSlice.value.hasSnapshot && !connectionSlice.value.error
    && !configured.value && !dismissed.value
})

function validate(conn: string, inst: string): Record<string, string> {
  const next: Record<string, string> = {}
  if (!conn) next.connName = 'A name is required.'
  else if (!DNS_LABEL_RE.test(conn)) next.connName = 'Lowercase letters, digits and dashes only.'
  else if (props.store.connections.data.some(c => c.metadata.name === conn)) next.connName = 'A connection with that name already exists.'
  if (!inst) next.instance = 'A name is required.'
  else if (!DNS_LABEL_RE.test(inst)) next.instance = 'Lowercase letters, digits and dashes only.'
  if (agents.value.length > 1 && !agent.value) next.agent = 'Pick the agent that should do this.'
  return next
}

async function grantToAgent(agentName: string, connection: string, authority: AuthoritySnapshot): Promise<void> {
  const target = authority.store.agents.data.find(item => item.metadata.name === agentName)
  if (!target) return
  const current = target.spec?.tools?.interactive?.connections || []
  if (current.includes(connection)) return
  const connections = [...current, connection]
  const families = familiesForConns(connections, name =>
    name === connection ? 'websearch' : authority.store.connections.data.find(item => item.metadata.name === name)?.spec.type,
  )
  await mutate(authority.store, {
    run: () => authority.api.patchAgent(agentName, { interactiveConnections: connections, interactiveFamilies: families }),
    success: `Wired “${connection}” into ${agentName}'s tools.`,
    failure: `Created the connection, but could not wire it into ${agentName} — add it under the agent's Tools`,
    reload: ['agents'],
  })
}

async function submit(): Promise<void> {
  if (busy.value) return
  const connection = connName.value.trim()
  const instanceValue = instanceName.value
  const agentName = selectedAgent.value
  errors.value = validate(connection, instanceValue)
  if (Object.keys(errors.value).length) return
  agent.value = agentName
  const authority = captureAuthority()
  const body: ConnectionWrite = { type: 'websearch', name: connection, config: { provider: 'searxng', instance: instanceValue } }
  busy.value = true
  try {
    const result = await mutate(authority.store, {
      run: () => authority.api.createConnection(body),
      success: `Connection “${connection}” created — handing the rest to ${agentName}.`,
      failure: 'Create failed',
      reload: ['connections'],
    })
    if (!result || !authorityIsCurrent(authority)) return
    await grantToAgent(agentName, connection, authority)
    if (!authorityIsCurrent(authority)) return
    authority.store.setPendingPrompt(agentName, searxngSetupPrompt({ connection, instance: instanceValue, size: size.value }))
    emit('create-success', {
      resource: 'connection', name: connection, item: result,
      destination: { kind: 'agent', name: agentName, tab: 'config' },
      store: props.store, authorityEpoch: props.authorityEpoch, createSession: props.createSession,
    })
  } finally {
    busy.value = false
  }
}

function cancel(): void { if (!busy.value) emit('create-cancel', { store: props.store, authorityEpoch: props.authorityEpoch, createSession: props.createSession }) }
function dismiss(): void {
  rememberAssistDismissed(readTenant().workspaceUUID)
  dismissed.value = true
}
</script>

<template>
  <template v-if="page">
    <div v-if="!infrastructureEnabled" class="agents-panel k-card agents-route-panel"><p class="muted">Infrastructure is not enabled for this workspace.</p></div>
    <div v-else-if="!agents.length" class="agents-panel k-card agents-route-panel"><p class="muted">Create an agent before using assisted search setup.</p></div>
    <div v-else-if="connectionSlice.error" class="agents-panel k-card agents-route-panel agents-state agents-state-error" role="alert">
      <p>Could not verify existing connections. {{ connectionSlice.error }}</p>
      <button class="k-btn k-btn--ghost secondary" type="button" :disabled="connectionSlice.loading" @click="store.load('connections')">{{ connectionSlice.loading ? 'Retrying…' : 'Retry' }}</button>
    </div>
    <div v-else-if="!connectionSlice.hasSnapshot" class="agents-panel k-card agents-route-panel k-loading-reveal" role="status"><p class="muted">Loading connections…</p></div>
    <div v-else-if="configured" class="agents-panel k-card agents-route-panel"><p class="muted">Self-hosted search is already configured in this workspace.</p></div>
    <div v-else class="agents-create-page k-create-page">
      <button type="button" :disabled="busy" class="k-btn k-btn--ghost k-back-action" @click="cancel"><ArrowLeft :stroke-width="1.75" /> Connections</button>
      <header class="k-create-header"><h1 class="k-create-title">Set up assisted search</h1><p class="k-create-description">Create a private search connection and let an agent provision the supporting service.</p></header>
      <form class="agents-conn-form agents-guided-form k-create-surface" aria-label="Assisted search setup" @submit.prevent="submit">
        <div class="k-create-body">
          <p class="muted">We create the web-search connection pointing at an instance name, then your agent provisions the <code>searxng</code> instance itself. Nothing to paste back: agents reach it over the platform's internal path, so it is never published and has no token.</p>
          <label v-if="agents.length > 1"><span id="assisted-search-agent-label">Agent</span>
            <FormSelect v-model="agent" :options="agentOptions" :disabled="busy" :invalid="!!errors.agent" labelledby="assisted-search-agent-label" :describedby="errors.agent ? 'assisted-search-agent-error' : undefined" />
            <span v-if="errors.agent" id="assisted-search-agent-error" class="agents-fielderr" role="alert">{{ errors.agent }}</span>
          </label>
          <p v-else class="agents-hint">Driven by your agent <strong>{{ selectedAgent }}</strong>.</p>
          <label for="assisted-search-connection-name">Connection name
            <input id="assisted-search-connection-name" v-model="connName" class="k-input" name="connName" :disabled="busy" autocomplete="off" :aria-invalid="errors.connName ? 'true' : undefined" :aria-describedby="errors.connName ? 'assisted-search-connection-name-error' : 'assisted-search-connection-name-hint'" />
            <span v-if="errors.connName" id="assisted-search-connection-name-error" class="agents-fielderr" role="alert">{{ errors.connName }}</span><span v-else id="assisted-search-connection-name-hint" class="agents-hint">What agents reference to search the web.</span>
          </label>
          <label for="assisted-search-instance-name">Instance name
            <input id="assisted-search-instance-name" class="k-input" name="instance" :value="instanceName" :disabled="busy" autocomplete="off" :aria-invalid="errors.instance ? 'true' : undefined" :aria-describedby="errors.instance ? 'assisted-search-instance-name-error' : 'assisted-search-instance-name-hint'" @input="instanceTouched = true; instance = ($event.target as HTMLInputElement).value" />
            <span v-if="errors.instance" id="assisted-search-instance-name-error" class="agents-fielderr" role="alert">{{ errors.instance }}</span><span v-else id="assisted-search-instance-name-hint" class="agents-hint">The infrastructure instance the agent provisions.</span>
          </label>
          <label>Size
            <div class="agents-modeseg" role="group" aria-label="Instance size">
              <button v-for="option in INSTANCE_SIZES" :key="option" type="button" :disabled="busy" :class="['k-btn k-btn--ghost agents-modebtn', { sel: option === size }]" :aria-pressed="option === size" @click="size = option">{{ option }}</button>
            </div>
          </label>
        </div>
        <div class="k-create-actions">
          <button type="button" :disabled="busy" class="k-btn k-btn--ghost secondary" @click="cancel">Cancel</button>
          <button class="k-btn k-btn--primary" type="submit" :disabled="busy"><Sparkles :stroke-width="1.75" /> Create and hand off</button>
        </div>
      </form>
    </div>
  </template>
  <div v-else-if="visibleCard" class="agents-assist">
    <span class="agents-assist-ic"><Sparkles :stroke-width="1.75" /></span>
    <div class="agents-assist-body"><strong>Set up self-hosted search with an agent</strong><span class="muted">One of your agents can provision the SearXNG instance for you — instead of you hopping to Infrastructure and back.</span></div>
    <button type="button" class="k-btn k-btn--ghost secondary" @click="emit('navigate', { kind: 'create', resource: 'connection', type: 'assisted-search' })">Set it up</button>
    <button type="button" class="k-btn k-btn--ghost agents-iconbtn" aria-label="Dismiss this suggestion" title="Dismiss — you can still add a web-search connection above" @click="dismiss"><X :stroke-width="1.75" /></button>
  </div>
</template>
