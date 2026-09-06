<script setup lang="ts">
import { AlertCircle, ArrowLeft, Check, Link, Megaphone, Plug, Plus, Send, Shuffle, Wrench } from 'lucide-vue-next'
import { computed, onBeforeUnmount, ref, watch, type Component } from 'vue'
import type { ApiClient } from '../api'
import SecretHandoff from '../components/SecretHandoff.vue'
import { CATEGORY_META, CONN_DEFS, SLACK_SIGNING_SECRET_FIELD, connCategory, connShape, isSecretBearingWebhook, type ConnCategory, type ConnField, type ConnTypeDef } from '../conn-defs'
import { mutate } from '../mutate'
import { confirmDialog } from '../portalkit/confirm'
import CreateGuidance from '../portalkit/CreateGuidance.vue'
import FirstRunGuide from '../portalkit/FirstRunGuide.vue'
import ResourceBackLink from '../portalkit/ResourceBackLink.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableActionButton from '../portalkit/ResourceTableActionButton.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import ResourceTableEditButton from '../portalkit/ResourceTableEditButton.vue'
import type { TableFilterDefinition } from '../portalkit/table'
import { hashFor, type CreateSuccessDetail, type EditCancelDetail, type EditSuccessDetail, type Route } from '../router'
import type { AppStore } from '../store'
import { toast } from '../ui/toast'
import type { Agent, Connection, ConnectionWrite, Toolset } from '../types'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'
import AssistedSearch from './AssistedSearch.vue'
import Toolsets from './Toolsets.vue'

interface Fence { store: AppStore; authorityEpoch?: number; createSession?: number }
const props = withDefaults(defineProps<{
  store: AppStore; api: ApiClient; routeOwned?: boolean; createRoute?: boolean; createType?: string
  editRoute?: boolean; editName?: string; authorityEpoch?: number; createSession?: number
}>(), { routeOwned: false, createRoute: false, createType: '', editRoute: false, editName: '' })
const emit = defineEmits<{
  navigate: [route: Route]
  'create-success': [detail: CreateSuccessDetail & Fence]
  'create-cancel': [detail: Fence]
  'edit-success': [detail: EditSuccessDetail & Fence]
  'edit-cancel': [detail: EditCancelDetail & Fence]
}>()
const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)
const connMode = ref('')
const createBusy = ref(false)
const editBusy = ref(false)
const actionBusy = ref('')
const deletePendingName = ref('')
const deletingName = ref('')
const createValues = ref<Record<string, string>>({})
const editValues = ref<Record<string, string>>({})
const editValuesFor = ref('')
const slackRequestURL = ref('')
const fence = (): Fence => ({ store: props.store, authorityEpoch: props.authorityEpoch, createSession: props.createSession })

const connections = computed(() => { revision.value; return { ...props.store.connections } })
const currentEdit = computed(() => connections.value.data.find(item => item.metadata.name === props.editName))
const editTitle = computed(() => currentEdit.value?.spec.displayName || currentEdit.value?.metadata.name || props.editName)
const editReadError = computed(() => {
  if (!connections.value.error) return null
  return connections.value.hasSnapshot
    ? connections.value.error
    : `Could not load this connection. ${connections.value.error}`
})
const createDef = computed(() => CONN_DEFS.find(def => def.id === props.createType))
const showFirstRun = computed(() => connections.value.loaded && connections.value.data.length === 0 && (!connections.value.error || connections.value.hasSnapshot))
const filters: TableFilterDefinition[] = [
  { key: 'kind', label: 'Kind', allLabel: 'All kinds' },
  { key: 'type', label: 'Type', allLabel: 'All types' },
]
const categoryIcons: Record<ConnCategory, Component> = { tool: Wrench, channel: Megaphone, connection: Plug }

function endpoint(item: Connection): string {
  if (isSecretBearingWebhook(item)) return 'Configured'
  return item.spec.config?.instance || item.spec.baseURL || item.spec.channel || ''
}
function selfHostedSearch(item: Connection): boolean { return item.spec.type === 'websearch' && item.spec.config?.provider === 'searxng' }
function instanceBacked(item: Connection): boolean { return selfHostedSearch(item) || (item.spec.type === 'mcp' && !!item.spec.config?.instance) }
function needsInstance(item: Connection): boolean { return selfHostedSearch(item) && !item.spec.config?.instance }
function usesChannel(item: Connection): boolean { return connCategory(item.spec.type) === 'channel' || !!item.spec.channel }
function endpointLabel(item: Connection): string {
  const shape = connShape(item)
  if (shape.discordWebhook) return 'Webhook URL'
  if (shape.discordBot) return 'Channel ID (optional)'
  if (!usesChannel(item)) return 'Endpoint URL'
  if (item.spec.type === 'slack') return 'Webhook URL / channel'
  if (item.spec.type === 'smtp') return 'Send to'
  return 'Channel / chat ID'
}
function wiringState(item: Connection, agents: Agent[], toolsets: Toolset[], agentsOK: boolean, toolsetsOK: boolean): 'wired' | 'unwired' | 'unknown' | null {
  if (connCategory(item.spec.type) !== 'tool') return null
  if (!agentsOK) return 'unknown'
  const name = item.metadata.name
  if (agents.some(agent => (agent.spec?.tools?.interactive?.connections || []).includes(name) || (agent.spec?.tools?.background?.connections || []).includes(name))) return 'wired'
  const granted = new Set(agents.flatMap(agent => [...(agent.spec?.tools?.interactive?.toolsets || []), ...(agent.spec?.tools?.background?.toolsets || [])]))
  if (!granted.size) return 'unwired'
  if (!toolsetsOK) return 'unknown'
  return toolsets.some(toolset => granted.has(toolset.metadata.name) && (toolset.spec.connections || []).includes(name)) ? 'wired' : 'unwired'
}
const rows = computed<Array<Record<string, unknown>>>(() => {
  revision.value
  return connections.value.data.map(item => ({
    id: item.metadata.name,
    name: `${item.spec.displayName || item.metadata.name} ${item.spec.displayName ? item.metadata.name : ''}`,
    kind: CATEGORY_META[connCategory(item.spec.type)].label, type: connShape(item).typeLabel, endpoint: endpoint(item), item,
  }))
})
const asConnection = (row: Record<string, unknown>): Connection => row.item as Connection
function wiring(item: Connection) {
  revision.value
  return wiringState(item, props.store.agents.data, props.store.toolsets.data, props.store.agents.hasSnapshot && !props.store.agents.error, props.store.toolsets.hasSnapshot && !props.store.toolsets.error)
}

watch(() => props.createType, () => { createValues.value = {}; connMode.value = '' })
watch(() => props.createSession, () => {
  createValues.value = {}; connMode.value = ''; editValuesFor.value = ''; editValues.value = {}
})
watch([() => props.editRoute, () => props.editName, () => revision.value], () => {
  if (!props.editRoute || !props.editName || editValuesFor.value === props.editName) return
  const item = props.store.connections.data.find(connection => connection.metadata.name === props.editName)
  if (!item) return
  hydrateEdit(item)
}, { immediate: true })

function selectType(def: ConnTypeDef): void {
  emit('navigate', { kind: 'create', resource: 'connection', type: def.id })
}
function setCreateValue(key: string, value: string): void { createValues.value = { ...createValues.value, [key]: value } }
function activeMode(def: ConnTypeDef) { return def.modes?.find(mode => mode.id === (def.modes?.some(item => item.id === connMode.value) ? connMode.value : def.modes?.[0]?.id || '')) }
function fieldsFor(def: ConnTypeDef): ConnField[] {
  revision.value
  let fields = def.modes ? activeMode(def)?.fields || [] : def.fields || []
  if (fields.some(field => field.key === 'clientID') && props.store.oauthApps.has(def.id)) fields = fields.filter(field => field.key !== 'clientID' && field.key !== 'clientSecret')
  return fields
}
function advancedFor(def: ConnTypeDef): ConnField[] { return [...(def.advanced || []), ...(activeMode(def)?.advanced || [])] }

async function create(def: ConnTypeDef): Promise<void> {
  if (createBusy.value) return
  const values = Object.fromEntries(Object.entries(createValues.value).map(([key, value]) => [key, value.trim()]))
  const mode = activeMode(def)?.id || ''
  const body = def.build(values, mode)
  createBusy.value = true
  try {
    const result = await mutate(props.store, { run: () => props.api.createConnection(body), success: 'Connection created.', failure: 'Create failed', reload: ['connections'] })
    if (result) emit('create-success', { resource: 'connection', name: body.name, item: result, ...fence() })
  } finally { createBusy.value = false }
}
function hydrateEdit(item: Connection): void {
  const usesChannel = connCategory(item.spec.type) === 'channel' || Boolean(item.spec.channel)
  editValuesFor.value = item.metadata.name
  editValues.value = { displayName: item.spec.displayName || '', endpoint: isSecretBearingWebhook(item) ? '' : (usesChannel ? item.spec.channel : item.spec.baseURL) || '', instance: item.spec.config?.instance || '', secret: '', signingSecret: '' }
}
// Slack signs Events API requests with the app signing secret and the provider
// refuses inbound events it cannot verify, so an existing Slack chat connection
// must be able to add or rotate it here. Incoming-webhook Slack connections are
// send-only and never receive events, so they don't take one.
function takesSigningSecret(item: Connection): boolean {
  return item.spec.type === 'slack' && !isSecretBearingWebhook(item)
}
async function saveEdit(item: Connection): Promise<void> {
  if (editBusy.value) return
  const authority = captureAuthority()
  const usesChannel = connCategory(item.spec.type) === 'channel' || !!item.spec.channel
  const get = (key: string) => (editValues.value[key] || '').trim()
  const patch: ConnectionWrite = { displayName: get('displayName') }
  if (instanceBacked(item)) patch.config = { ...item.spec.config, instance: get('instance') }
  else if (usesChannel) {
    if (get('endpoint')) patch.channel = get('endpoint')
  } else patch.baseURL = get('endpoint')
  if (get('secret')) patch.secret = get('secret')
  if (takesSigningSecret(item) && get('signingSecret')) patch.signingSecret = get('signingSecret')
  editBusy.value = true
  try {
    const result = await mutate(authority.store, { run: () => authority.api.patchConnection(item.metadata.name, patch), success: 'Connection updated.', failure: 'Update failed', reload: ['connections'] })
    if (!result || !authorityIsCurrent(authority)) return
    editValuesFor.value = ''; editValues.value = {}
    emit('edit-success', { resource: 'connection', name: item.metadata.name, item: result, ...fence() })
  } finally { editBusy.value = false }
}
async function remove(name: string): Promise<void> {
  if (actionBusy.value || deletePendingName.value || deletingName.value) return
  const authority = captureAuthority()
  deletePendingName.value = name
  const ok = await confirmDialog({ title: `Delete connection “${name}”?`, danger: true, confirmLabel: 'Delete' })
  deletePendingName.value = ''
  if (!ok || !authorityIsCurrent(authority) || actionBusy.value || deletingName.value) return
  deletingName.value = name
  try {
    await mutate(authority.store, { run: () => authority.api.deleteConnection(name), success: 'Connection deleted.', failure: 'Delete failed', reload: ['connections'] })
  } finally {
    deletingName.value = ''
  }
}
async function test(name: string): Promise<void> {
  if (actionBusy.value || deletePendingName.value || deletingName.value) return
  const authority = captureAuthority()
  const operation = `test:${name}`
  actionBusy.value = operation
  try {
    await mutate(authority.store, { run: () => authority.api.testConnection(name), success: `Test message sent via ${name}. Check the channel.`, failure: `Test of “${name}” failed` })
  } finally {
    if (actionBusy.value === operation) actionBusy.value = ''
  }
}
async function enableInbound(name: string): Promise<void> {
  if (actionBusy.value || deletePendingName.value || deletingName.value) return
  const authority = captureAuthority()
  const operation = `inbound:${name}`
  actionBusy.value = operation
  try {
    const result = await mutate(authority.store, { run: () => authority.api.enableInbound(name), failure: 'Enable inbound failed', reload: ['connections'] })
    if (result && authorityIsCurrent(authority)) {
      const connection = connections.value.data.find(item => item.metadata.name === name)
      if (connection?.spec.type === 'slack' && result.webhookURL) slackRequestURL.value = result.webhookURL
      toast(result.registered ? 'ok' : 'info', result.note)
    }
  } finally {
    if (actionBusy.value === operation) actionBusy.value = ''
  }
}
watch(() => [props.store, props.authorityEpoch], () => { slackRequestURL.value = '' })
onBeforeUnmount(() => { slackRequestURL.value = '' })
async function oauth(name: string): Promise<void> {
  if (actionBusy.value || deletePendingName.value || deletingName.value) return
  const authority = captureAuthority()
  const operation = `oauth:${name}`
  actionBusy.value = operation
  try {
    const result = await mutate(authority.store, { run: () => authority.api.oauthAuthorize(name), failure: 'OAuth connect failed' })
    if (!result || !authorityIsCurrent(authority)) return
    window.open(result.authorizeURL, '_blank', 'noopener'); toast('info', 'Authorize in the opened tab, then refresh.')
  } finally {
    if (actionBusy.value === operation) actionBusy.value = ''
  }
}
function openEdit(item: Connection): void {
  emit('navigate', { kind: 'edit', resource: 'connection', name: item.metadata.name })
}
function cancelCreate(): void {
  if (createBusy.value) return
  createValues.value = {}
  emit('create-cancel', fence())
}
function cancelEdit(): void {
  if (editBusy.value) return
  editValuesFor.value = ''; editValues.value = {}
  emit('edit-cancel', { resource: 'connection', name: props.editName, ...fence() })
}
function forwardCreate(detail: CreateSuccessDetail & Partial<Fence>): void { emit('create-success', { ...detail, ...fence() }) }
</script>

<template>
  <div v-if="createRoute && !createType" class="agents-menu agents-create-page k-create-page">
    <button type="button" class="k-btn k-btn--ghost k-back-action" :disabled="createBusy" @click="cancelCreate"><ArrowLeft :stroke-width="1.75" /> Connections</button>
    <header class="k-create-header"><h1 class="k-create-title">Create connection</h1><p class="k-create-description">Choose the tool, channel, or external service you want agents to use.</p></header>
    <div class="k-create-surface k-create-surface--wide"><div class="k-create-body"><div class="agents-conn-picker">
      <div v-for="category in (['tool', 'channel', 'connection'] as ConnCategory[])" :key="category" class="agents-conn-group">
        <h2 class="agents-conn-grouphead"><component :is="categoryIcons[category]" :stroke-width="1.75" /> {{ CATEGORY_META[category].label }}s <span class="muted">— {{ CATEGORY_META[category].blurb }}</span></h2>
        <div class="agents-conn-types"><button v-for="definition in CONN_DEFS.filter(item => connCategory(item.id) === category)" :key="definition.id" class="k-btn k-btn--ghost agents-conn-tile" @click="selectType(definition)"><span class="agents-conn-glyph"><Plug :stroke-width="1.75" /></span><span class="agents-conn-name">{{ definition.label }}</span><span class="muted">{{ definition.desc }}</span></button></div>
        <AssistedSearch v-if="category === 'tool'" :store="store" :api="api" @navigate="emit('navigate', $event)" />
      </div>
    </div></div></div>
  </div>
  <div v-else-if="createRoute && createType === 'assisted-search'" class="agents-menu agents-create-page">
    <AssistedSearch :store="store" :api="api" page :authority-epoch="authorityEpoch" :create-session="createSession" @create-success="forwardCreate" @create-cancel="cancelCreate" />
  </div>
  <div v-else-if="createRoute && !createDef" class="agents-create-page k-create-page">
    <button type="button" class="k-btn k-btn--ghost k-back-action" :disabled="createBusy" @click="cancelCreate"><ArrowLeft :stroke-width="1.75" /> Connections</button>
    <header class="k-create-header"><h1 class="k-create-title">Connection type unavailable</h1><p class="k-create-description">That connection type is not available in this version of the Agents provider.</p></header>
  </div>
  <div v-else-if="createRoute && createDef" class="agents-menu agents-create-page k-create-page">
    <button type="button" class="k-btn k-btn--ghost k-back-action" :disabled="createBusy" @click="cancelCreate"><ArrowLeft :stroke-width="1.75" /> Connections</button>
    <header class="k-create-header"><h1 class="k-create-title">Connect {{ createDef.label }}</h1><p class="k-create-description">{{ createDef.desc }}</p></header>
    <form class="agents-conn-form agents-guided-form k-create-surface k-create-surface--guided" :aria-busy="createBusy" @submit.prevent="create(createDef)">
      <div class="k-create-body k-create-body--guided"><div class="k-create-fields">
        <div class="agents-conn-formhead"><button type="button" class="k-btn k-btn--ghost agents-back" :disabled="createBusy" @click="emit('navigate', { kind: 'create', resource: 'connection' })"><ArrowLeft :stroke-width="1.75" /> connection types</button></div>
        <details v-if="createDef.setup" class="agents-setup" open><summary>Before you start — setup steps</summary><ol><li v-for="step in createDef.setup" :key="step" v-html="step" /></ol></details>
        <label>Name *<input class="k-input" name="name" required pattern="[a-z0-9-]+" :placeholder="`my-${createDef.id}`" autocomplete="off" :value="createValues.name || ''" :disabled="createBusy" @input="setCreateValue('name', ($event.target as HTMLInputElement).value)" /><span class="agents-hint">A short id you'll reference from agents.</span></label>
        <div v-if="createDef.modes" class="agents-modeseg" role="group" aria-label="Authentication mode"><button v-for="mode in createDef.modes" :key="mode.id" type="button" :class="['k-btn k-btn--ghost agents-modebtn', { sel: mode.id === activeMode(createDef)?.id }]" :disabled="createBusy" :aria-pressed="mode.id === activeMode(createDef)?.id" @click="connMode = mode.id">{{ mode.label }}</button></div>
        <div v-if="fieldsFor(createDef).some(field => field.key === 'clientID') === false && activeMode(createDef)?.fields.some(field => field.key === 'clientID') && store.oauthApps.has(createDef.id)" class="agents-platform-note"><Check :stroke-width="1.75" /> Using the platform's {{ createDef.label }} OAuth app — no client id/secret needed. Create it, then click <strong>Connect</strong>.</div>
        <label v-for="field in fieldsFor(createDef)" :key="field.key">{{ field.label }}{{ field.required ? ' *' : '' }}<input class="k-input" :name="field.key" :type="field.password ? 'password' : 'text'" :placeholder="field.placeholder || ''" :required="field.required" autocomplete="off" :value="createValues[field.key] || ''" :disabled="createBusy" @input="setCreateValue(field.key, ($event.target as HTMLInputElement).value)" /><span v-if="field.hint" class="agents-hint">{{ field.hint }}</span></label>
        <details v-if="advancedFor(createDef).length" class="agents-adv"><summary>Advanced</summary><label v-for="field in advancedFor(createDef)" :key="field.key">{{ field.label }}{{ field.required ? ' *' : '' }}<input class="k-input" :name="field.key" :type="field.password ? 'password' : 'text'" :placeholder="field.placeholder || ''" :required="field.required" autocomplete="off" :value="createValues[field.key] || ''" :disabled="createBusy" @input="setCreateValue(field.key, ($event.target as HTMLInputElement).value)" /><span v-if="field.hint" class="agents-hint">{{ field.hint }}</span></label></details>
      </div><CreateGuidance :title="`Connect ${createDef.label}`" description="Prepare the external identity once; agents receive access only when you grant this connection later." :prerequisites="[createDef.setup?.length ? 'Review the provider setup steps shown in the form.' : `Have the ${createDef.label} endpoint or credential ready.`, `Required fields: ${['Name', ...fieldsFor(createDef).filter(field => field.required).map(field => field.label)].join(', ')}.`]" :values="[{ label: 'Connection', value: createValues.name?.trim() || 'Not entered yet', technical: true }, { label: 'Type', value: createDef.label }, { label: 'Mode', value: activeMode(createDef)?.label || 'Default' }, { label: 'Required details', value: `${fieldsFor(createDef).filter(field => field.required && createValues[field.key]?.trim()).length} of ${fieldsFor(createDef).filter(field => field.required).length} entered` }]" :next-steps="['Faros stores secret values in this workspace and does not show them after creation.', 'Test or authorize the connection from the Connections page when the type supports it.', 'Grant the connection to an agent directly or add it to a shared toolset.']" /></div>
      <div class="k-create-actions"><button type="button" class="k-btn k-btn--ghost secondary" :disabled="createBusy" @click="cancelCreate">Cancel</button><button class="k-btn k-btn--primary" type="submit" :disabled="createBusy">{{ createBusy ? 'Creating…' : 'Create connection' }}</button></div>
    </form>
  </div>
  <div v-else-if="editRoute" class="agents-detail">
    <ResourceBackLink :href="hashFor({ kind: 'menu', menu: 'connections' })" :disabled="editBusy" @back="cancelEdit">Connections</ResourceBackLink>
    <ResourcePage
      :title="editTitle"
      kind="Connection"
      subtitle="Update this connection without exposing its stored credential."
      :loaded="connections.hasSnapshot"
      :loading="connections.loading"
      :error="editReadError"
      :stale="connections.hasSnapshot && !!connections.error"
      retryable
      @retry="store.load('connections')"
    >
      <template v-if="currentEdit && editTitle !== currentEdit.metadata.name" #meta><code>{{ currentEdit.metadata.name }}</code></template>
      <template #body>
        <div v-if="!currentEdit" class="k-card agents-state agents-state-empty" role="status">
          Connection “{{ editName }}” was not found in {{ connections.error ? 'the last loaded workspace snapshot' : 'this workspace' }}.
        </div>
        <ResourceSectionCard v-else title="Connection settings" description="Change the endpoint or display name, or rotate the stored credential.">
          <form class="agents-conn-form" :aria-busy="editBusy" @submit.prevent="saveEdit(currentEdit)">
            <div class="k-create-body k-create-fields">
              <label>Display name<input v-model="editValues.displayName" class="k-input" name="displayName" :placeholder="currentEdit.metadata.name" :disabled="editBusy" /></label>
              <label v-if="instanceBacked(currentEdit)">Instance name *<input v-model="editValues.instance" class="k-input" name="instance" placeholder="search" required autocomplete="off" :disabled="editBusy" /><span class="agents-hint">The instance under Infrastructure. Agents reach it over the platform's internal path — there is no URL and no token.</span></label>
              <label v-else>{{ isSecretBearingWebhook(currentEdit) ? 'Replacement webhook URL' : endpointLabel(currentEdit) }}<input v-model="editValues.endpoint" class="k-input" name="endpoint" :placeholder="isSecretBearingWebhook(currentEdit) ? 'Configured — leave blank to keep it' : ''" :disabled="editBusy" /><span v-if="isSecretBearingWebhook(currentEdit)" class="agents-hint">Configured; leave blank to keep the current destination.</span></label>
              <p v-if="!instanceBacked(currentEdit) && !connShape(currentEdit).discordWebhook && currentEdit.spec.auth === 'oauth'" class="agents-hint">This is an OAuth connection — use the <Link :stroke-width="1.75" /> button in the table to re-authorize. Client credentials aren't edited here.</p>
              <label v-else-if="!instanceBacked(currentEdit) && !connShape(currentEdit).discordWebhook">New {{ connShape(currentEdit).discordBot ? 'bot token' : 'secret / token' }}<input v-model="editValues.secret" class="k-input" name="secret" type="password" placeholder="leave blank to keep the current one" autocomplete="off" :disabled="editBusy" /><span class="agents-hint">Only set this to rotate the credential.</span></label>
              <label v-if="takesSigningSecret(currentEdit)">{{ SLACK_SIGNING_SECRET_FIELD.label }}<input v-model="editValues.signingSecret" class="k-input" name="signingSecret" type="password" placeholder="leave blank to keep the current one" autocomplete="off" :disabled="editBusy" /><span class="agents-hint">{{ SLACK_SIGNING_SECRET_FIELD.hint }}</span></label>
            </div>
            <div class="k-create-actions"><button type="button" class="k-btn k-btn--ghost secondary" :disabled="editBusy" @click="cancelEdit">Cancel</button><button class="k-btn k-btn--primary" type="submit" :disabled="editBusy">{{ editBusy ? 'Saving…' : 'Save changes' }}</button></div>
          </form>
        </ResourceSectionCard>
      </template>
    </ResourcePage>
  </div>
  <div v-else class="agents-menu">
    <div class="agents-panel k-card agents-route-panel">
      <div class="agents-panel-head"><h3 tabindex="-1" data-connections-heading>Connections</h3><button v-if="!showFirstRun" class="k-btn k-btn--primary" @click="emit('navigate', { kind: 'create', resource: 'connection' })"><Plus :stroke-width="1.75" /> New connection</button></div>
      <SecretHandoff v-if="slackRequestURL" :value="slackRequestURL" label="Slack request URL" copy-label="Copy Slack request URL" @cleared="slackRequestURL = ''" />
      <p class="muted agents-connections-copy">Shared credentials for external systems. Each is a <Wrench :stroke-width="1.75" /> <strong>Tool</strong> agents call, a <Megaphone :stroke-width="1.75" /> <strong>Channel</strong> they message you on, or a <Plug :stroke-width="1.75" /> generic <strong>Connection</strong>. Stored as Secrets in your workspace.</p>
      <template v-if="showFirstRun">
        <div v-if="connections.error" class="agents-stale" role="status">
          <AlertCircle aria-hidden="true" /> Showing the last loaded connections. {{ connections.error }}
          <button class="k-btn k-btn--ghost" type="button" :disabled="connections.loading" @click="store.load('connections')">{{ connections.loading ? 'Retrying…' : 'Retry' }}</button>
        </div>
        <FirstRunGuide title="Connect tools and channels" description="Add reusable access to an external system once, then grant it to agents directly or through a toolset." primary-label="Create connection" :steps="[{ label: 'Connection', description: 'Tool, channel, or external credential' }, { label: 'Toolset', description: 'Optional reusable capability bundle' }, { label: 'Agent', description: 'Grant access from agent Config' }]" :current-step="0" journey-label="Connection setup path" @primary="emit('navigate', { kind: 'create', resource: 'connection' })"><template #icon><Plug :stroke-width="1.75" /></template></FirstRunGuide>
      </template>
      <ResourceTable v-else :columns="[{ key: 'name', label: 'Name', primary: true }, { key: 'kind', label: 'Kind' }, { key: 'type', label: 'Type' }, { key: 'endpoint', label: 'Endpoint / channel' }, { key: 'actions', label: '', ariaLabel: 'Actions' }]" :rows="rows" row-key="id" aria-label="Connections" :loaded="connections.hasSnapshot" :loading="connections.loading" :error="connections.error" :stale="connections.hasSnapshot && !!connections.error" retryable searchable search-placeholder="Search connections…" :search-keys="['name', 'type', 'endpoint']" :filters="filters" paginated :interactive="false" @retry="store.load('connections')">
        <template #name="{ row }"><span class="agents-resource-name" :title="asConnection(row).metadata.name">{{ asConnection(row).spec.displayName || asConnection(row).metadata.name }}</span><code v-if="asConnection(row).spec.displayName" class="agents-resource-id">{{ asConnection(row).metadata.name }}</code><span v-if="asConnection(row).status?.webhookPath" class="agents-inbound-on" aria-label="Inbound enabled" data-k-tip="Inbound enabled"><Shuffle :stroke-width="1.75" /></span><span v-if="asConnection(row).status?.oauthConnected" class="agents-inbound-on" aria-label="OAuth connected" data-k-tip="OAuth connected"><Link :stroke-width="1.75" /></span></template>
        <template #kind="{ row }"><span :class="`k-badge agents-badge agents-badge-cat agents-cat-${connCategory(asConnection(row).spec.type)}`"><component :is="categoryIcons[connCategory(asConnection(row).spec.type)]" :stroke-width="1.75" /> {{ CATEGORY_META[connCategory(asConnection(row).spec.type)].label }}</span></template>
        <template #type="{ row }"><span class="k-badge agents-badge">{{ connShape(asConnection(row)).typeLabel }}</span></template>
        <template #endpoint="{ row }"><span class="agents-resource-endpoint" :title="endpoint(asConnection(row))">{{ endpoint(asConnection(row)) || '—' }}</span><span v-if="needsInstance(asConnection(row))" class="k-badge agents-badge k-badge--warning agents-badge-warn" title="Edit this connection and name the searxng instance it should search through">needs an instance</span><span v-if="wiring(asConnection(row)) === 'unwired'" class="k-badge agents-badge k-badge--warning agents-badge-warn" title="No agent has been granted this tool — add it under Tools in the agent's Config">not wired to an agent</span><span v-else-if="wiring(asConnection(row)) === 'unknown'" class="k-badge agents-badge k-badge--muted" title="Agent or toolset assignments are unavailable">wiring unknown</span></template>
        <template #actions="{ row }"><ResourceTableEditButton :label="`Edit connection ${asConnection(row).metadata.name}`" :disabled="!!actionBusy || !!deletePendingName || !!deletingName" @click="openEdit(asConnection(row))" /><ResourceTableActionButton v-if="connCategory(asConnection(row).spec.type) === 'channel'" :icon="Send" tone="accent" :label="`Send a test message via ${asConnection(row).metadata.name}`" :busy-label="`Sending a test message via ${asConnection(row).metadata.name}…`" :busy="actionBusy === `test:${asConnection(row).metadata.name}`" :disabled="!!actionBusy || !!deletePendingName || !!deletingName" @click="test(asConnection(row).metadata.name)" /><ResourceTableActionButton v-if="connCategory(asConnection(row).spec.type) === 'channel'" :icon="Shuffle" :label="`${asConnection(row).status?.webhookPath ? 'Re-enable' : 'Enable'} inbound chat for ${asConnection(row).metadata.name}`" :busy-label="`${asConnection(row).status?.webhookPath ? 'Re-enabling' : 'Enabling'} inbound chat for ${asConnection(row).metadata.name}…`" :busy="actionBusy === `inbound:${asConnection(row).metadata.name}`" :disabled="!!actionBusy || !!deletePendingName || !!deletingName" @click="enableInbound(asConnection(row).metadata.name)" /><ResourceTableActionButton v-if="asConnection(row).spec.auth === 'oauth'" :icon="Link" tone="accent" :label="`${asConnection(row).status?.oauthConnected ? 'Reconnect' : 'Connect'} OAuth for ${asConnection(row).metadata.name}`" :busy-label="`${asConnection(row).status?.oauthConnected ? 'Reconnecting' : 'Connecting'} OAuth for ${asConnection(row).metadata.name}…`" :busy="actionBusy === `oauth:${asConnection(row).metadata.name}`" :disabled="!!actionBusy || !!deletePendingName || !!deletingName" @click="oauth(asConnection(row).metadata.name)" /><ResourceTableDeleteButton :label="`Delete connection ${asConnection(row).metadata.name}`" :busy-label="`Deleting connection ${asConnection(row).metadata.name}…`" :busy="deletingName === asConnection(row).metadata.name" :disabled="!!actionBusy || !!deletePendingName || !!deletingName" @click="remove(asConnection(row).metadata.name)" /></template>
      </ResourceTable>
      <AssistedSearch :store="store" :api="api" @navigate="emit('navigate', $event)" />
    </div>
    <Toolsets :store="store" :api="api" :route-owned="routeOwned" @navigate="emit('navigate', $event)" @create-success="forwardCreate" @create-cancel="cancelCreate" />
  </div>
</template>
