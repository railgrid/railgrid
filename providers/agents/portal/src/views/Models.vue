<script setup lang="ts">
import snapshotSuffixPattern from '../model-snapshot-suffix.json'

import { Play, ArrowLeft, Check, Plus, Trash2 } from 'lucide-vue-next'
import { computed, ref, useId, watch } from 'vue'
import type { ApiClient } from '../api'
import { mutate } from '../mutate'
import { confirmDialog } from '../portalkit/confirm'
import ActionMenu, { type ActionMenuItem } from '../portalkit/ActionMenu.vue'
import FirstRunGuide from '../portalkit/FirstRunGuide.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableEditButton from '../portalkit/ResourceTableEditButton.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import ResourceTableExpandButton from '../portalkit/ResourceTableExpandButton.vue'
import ResourceTableActionButton from '../portalkit/ResourceTableActionButton.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import ModelCollection from '../agentkit/ModelCollection.vue'
import ModelConnectionCard from '../agentkit/ModelConnectionCard.vue'
import WorkspaceAssistantCard from './WorkspaceAssistantCard.vue'
import type { RailgridContext } from '../types'
import AgentUsage from './AgentUsage.vue'
import ModelConnectionEditor from './ModelConnectionEditor.vue'
import ModelDetails from './ModelDetails.vue'
import ProviderConnectionEditor from './ProviderConnectionEditor.vue'
import TechnicalDetails from '../components/TechnicalDetails.vue'
import { errorTechnicalDiagnostic, presentModelProbeResult } from '../model-probe-result'
import { hashFor, MODELS_TABS, type ModelsTab, type CreateSuccessDetail, type Route } from '../router'
import type { AppStore } from '../store'
import { toast } from '../ui/toast'
import { agentHarness, agentModelCredential, agentModelFallbacks, harnessLabel, isHarnessProvider, type Agent, type Credential, type CredentialWrite, type CredentialTestResult, type ModelInfo } from '../types'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'

interface Fence { store: AppStore; authorityEpoch?: number; createSession?: number }
const props = withDefaults(defineProps<{ store: AppStore; api: ApiClient; overlayControl?: RailgridContext['agentOverlay']; returnLabel?: string; selectConnection?: boolean; continueAddModelAfterProviderConnect?: boolean; modelsTab?: ModelsTab; routeOwned?: boolean; setupNavigation?: boolean; createRoute?: boolean; initialFamily?: 'chat' | 'harness'; authorityEpoch?: number; createSession?: number }>(), { modelsTab: 'models', routeOwned: false, createRoute: false, initialFamily: 'chat', returnLabel: 'Models', continueAddModelAfterProviderConnect: false })
const emit = defineEmits<{
  navigate: [route: Route]
  'connect-provider': [continueToModel: boolean]
  'create-success': [detail: CreateSuccessDetail & Fence]
  'create-cancel': [detail: Fence]
}>()
const localTab = ref<ModelsTab>(props.modelsTab)
const activeTab = computed(() => props.routeOwned ? props.modelsTab : localTab.value)
watch(() => props.modelsTab, tab => { localTab.value = tab })
function tabRoute(tab: ModelsTab): Route {
  return { kind: 'menu', menu: 'models', ...(tab === 'models' ? {} : { modelsTab: tab }) }
}
function navigateTab(event: MouseEvent, tab: ModelsTab): void {
  if (event.ctrlKey || event.metaKey || event.shiftKey || event.altKey || event.button !== 0) return
  event.preventDefault()
  openModelTab(tab)
}
function openModelTab(tab: ModelsTab): void {
  localTab.value = tab
  emit('navigate', tabRoute(tab))
}
const connectionView = computed(() => props.setupNavigation || activeTab.value === 'provider-connections')
const modelView = computed(() => props.setupNavigation || activeTab.value === 'models')
const credentialView = computed(() => modelView.value || connectionView.value)
const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)
const catalog = ref<ModelInfo[]>([])
const catalogError = ref<string | null>(null)
const catalogHasSnapshot = ref(false)
const catalogLoading = ref(false)
const tested = ref(new Map<string, CredentialTestResult>())
const testing = ref(new Set<string>())
type CredentialAction = 'saving' | 'deleting'
const credentialActions = ref(new Map<string, CredentialAction>())
const editName = ref<string | null>(null)
const initialConnection = ref('')
const selectingConnection = ref(Boolean(props.selectConnection))
const creating = ref(false)
const createBusy = ref(false)
const editorGeneration = ref(0)
const editor = ref<{ cancel: () => Promise<void>; locked: boolean } | null>(null)
const editingCredential = ref<Credential>()
const connectedProvider = ref<Credential | null>(null)
const continueAddedProvider = ref(false)
const savedProviderDiscovery = ref<{ connectionName: string; models: string[] } | null>(null)
const editorFamily = ref<'chat' | 'harness'>(props.initialFamily)
const saveError = ref<string | null>(null)
const expandedModel = ref<string | null>(null)
const modelDetailsPrefix = useId()
let catalogGeneration = 0
const probeGenerations = new Map<string, number>()

const credentials = computed(() => { revision.value; return { ...props.store.credentials } })

function resetCatalogRead(): void {
  catalogGeneration += 1
  catalog.value = []
  catalogError.value = null
  catalogHasSnapshot.value = false
  catalogLoading.value = false
}
async function loadCatalog(): Promise<void> {
  const generation = ++catalogGeneration
  const authority = captureAuthority()
  catalogLoading.value = true
  try {
    const result = await authority.api.catalog()
    if (generation !== catalogGeneration || !authorityIsCurrent(authority)) return
    catalog.value = result
    catalogHasSnapshot.value = true
    catalogError.value = null
  } catch (error) {
    if (generation !== catalogGeneration || !authorityIsCurrent(authority)) return
    catalogError.value = (error as Error).message
  } finally {
    if (generation === catalogGeneration && authorityIsCurrent(authority)) catalogLoading.value = false
  }
}
watch([() => props.store, () => props.api], () => { resetCatalogRead(); void loadCatalog() }, { immediate: true })
watch(() => props.store, () => { expandedModel.value = null })
watch([() => props.store, () => props.api, () => props.createSession], () => { editorGeneration.value++; initialConnection.value = ''; selectingConnection.value = Boolean(props.selectConnection); createBusy.value = false; credentialActions.value = new Map(); editingCredential.value = undefined; editName.value = null; creating.value = false; connectedProvider.value = null; continueAddedProvider.value = false; savedProviderDiscovery.value = null; editorFamily.value = props.initialFamily; saveError.value = null; tested.value = new Map(); testing.value = new Set() })

function lookupModel(model: string): ModelInfo | undefined {
  const normalized = (model || '').toLowerCase().trim().replace(/^.*\//, '')
  let exact: ModelInfo | undefined; let best: ModelInfo | undefined
  for (const item of catalog.value) {
    if (item.id === normalized) exact = item
    if (normalized.startsWith(item.id) && new RegExp(snapshotSuffixPattern).test(normalized.slice(item.id.length)) && (!best || item.id.length > best.id.length)) best = item
  }
  return exact || best
}
function missingCatalogLabel(): string {
  if (!catalogHasSnapshot.value) return catalogError.value ? 'catalog unavailable — pricing unknown' : 'catalog loading — pricing unknown'
  return catalogError.value ? 'not in last loaded catalog — pricing unknown' : 'not in catalog — no pricing'
}
// "Which agents use this credential?" now has two answers, because a credential
// is either a chat endpoint a model-backed agent reasons with or the identity a
// harness-backed agent's harness runs as. Both are a primary use of it.
function primaryName(agent: Agent): string { return agentHarness(agent)?.credentialRef || agentModelCredential(agent) }
function primaryOf(credential: Credential) { revision.value; return props.store.agents.data.filter(agent => primaryName(agent) === credential.name) }
function fallbackOf(credential: Credential) { revision.value; return props.store.agents.data.filter(agent => primaryName(agent) !== credential.name && agentModelFallbacks(agent).includes(credential.name)) }
function setMap<K, V>(source: Map<K, V>, key: K, value: V): Map<K, V> { return new Map(source).set(key, value) }
function credentialStateKey(name: string, connection = false): string { return `${connection ? 'connection' : 'model'}:${name}` }
function credentialAction(name: string, connection = false): CredentialAction | undefined { return credentialActions.value.get(credentialStateKey(name, connection)) }
// statusLabel is what the card says before anyone presses Test: the
// reconciler's own verdict. `ready` undefined means the provider has not
// looked yet, which is a different statement from "looked and it is broken" —
// so it reads as "Checking…", not as a failure.
function statusLabel(credential: Credential): string {
  if (harnessCredential(credential)) {
    if (credential.ready === true) return 'Credential checked'
    if (credential.ready === false) return 'Needs attention'
    return 'Checking credential…'
  }
  if (credential.ready === true) return 'Ready'
  if (credential.ready === false) return credential.secretResolved === false ? 'Needs credential' : 'Not reachable'
  return 'Checking…'
}
function statusTone(credential: Credential): 'success' | 'danger' | 'muted' {
  if (credential.ready === true) return 'success'
  if (credential.ready === false) return 'danger'
  return 'muted'
}
function credentialIsBusy(name: string, connection = false): boolean { return credentialActions.value.has(credentialStateKey(name, connection)) }
// Harness identities share the credential API type, but are managed separately
// from chat models and provider connections.
function harnessCredential(credential: Credential): boolean { return isHarnessProvider(credential.provider) }
function providerConnectionCredential(credential: Credential): boolean {
  return credential.isConnection === true && !harnessCredential(credential)
}
function credentialLabel(credential: Credential): string { return credential.displayName || credential.name }
function labelForName(name: string, connection = false): string {
  return credentials.value.data.find(item => item.name === name && providerConnectionCredential(item) === connection)?.displayName || name
}
const providerConnections = computed(() => credentials.value.data.filter(providerConnectionCredential))
const harnessIdentities = computed(() => credentials.value.data.filter(harnessCredential))
const showProviderFirstRun = computed(() => credentials.value.loaded && !providerConnections.value.length && !harnessIdentities.value.length && (!credentials.value.error || credentials.value.hasSnapshot))
const connectionRows = computed(() => [
  ...providerConnections.value.map(credential => ({
    id: `connection:${credential.name}`,
    name: credentialLabel(credential),
    type: 'API connection',
    endpoint: credential.baseURL || 'Provider default',
    modelCount: credentials.value.data.filter(item => !item.isConnection && !harnessCredential(item) && item.connectionRef === credential.name).length,
    status: statusLabel(credential),
    statusTone: statusTone(credential),
    credential,
  })),
  ...harnessIdentities.value.map(credential => ({
    id: `credential:${credential.name}`,
    name: credentialLabel(credential),
    type: `${harnessLabel(credential.provider)} identity`,
    endpoint: 'Edge machine',
    modelCount: '—',
    status: statusLabel(credential),
    statusTone: statusTone(credential),
    credential,
  })),
])
function connectionCredential(row: Record<string, unknown>): Credential { return row.credential as Credential }
function connectionModelCountSortValue(row: Record<string, unknown>): number | null {
  return typeof row.modelCount === 'number' ? row.modelCount : null
}
function modelProviderName(credential: Credential): string {
  if (!credential.connectionRef) return 'Direct credential'
  if (credential.connectionUnavailable) return 'Missing or invalid connection'
  const connection = providerConnections.value.find(item => item.name === credential.connectionRef)
  return connection ? credentialLabel(connection) : credential.connectionName || 'Connection details unavailable'
}
function toggleModelDetails(name: string): void {
  expandedModel.value = expandedModel.value === name ? null : name
}
const modelRows = computed(() => credentials.value.data.filter(item => !item.isConnection && !harnessCredential(item)).map(credential => ({
  id: credential.name, name: credentialLabel(credential), model: credential.model || '—', endpoint: credential.baseURL || 'Provider default',
  providerConnection: modelProviderName(credential),
  configured: credential.secretResolved !== false,
  status: testing.value.has(credentialStateKey(credential.name)) ? 'Testing…' : tested.value.get(credentialStateKey(credential.name))?.ok ? `Test passed · ${tested.value.get(credentialStateKey(credential.name))?.latencyMS} ms` : tested.value.has(credentialStateKey(credential.name)) ? 'Test failed' : statusLabel(credential),
  tone: tested.value.get(credentialStateKey(credential.name))?.ok ? 'success' as const : tested.value.has(credentialStateKey(credential.name)) ? 'danger' as const : statusTone(credential),
  credential,
})))
const showFirstRun = computed(() => credentials.value.loaded && (!credentials.value.error || credentials.value.hasSnapshot) &&
  (props.setupNavigation ? !modelRows.value.length && !providerConnections.value.length : !credentials.value.data.length))
const createActions: readonly ActionMenuItem[] = [
  { id: 'model', label: 'Add model', description: 'Use a saved provider connection' },
  { id: 'provider', label: 'Connect provider', description: 'Save API access for models' },
  { id: 'harness', label: 'Add harness identity', description: 'Set up a coding harness on an edge machine', separatorBefore: true },
]
const editorIsHarness = computed(() => editingCredential.value
  ? harnessCredential(editingCredential.value)
  : editorFamily.value === 'harness')
const editorTitle = computed(() => editorIsHarness.value
  ? editingCredential.value ? 'Edit harness identity' : 'Add harness identity'
  : editingCredential.value?.isConnection ? 'Edit provider connection' : editingCredential.value ? 'Edit model' : initialConnection.value || selectingConnection.value ? 'Add model' : 'Connect provider')
const editorDescription = computed(() => editorIsHarness.value
  ? 'The login a coding harness on an edge runs as.'
  : editingCredential.value?.isConnection ? 'Update the connection name or provider settings.'
    : editingCredential.value ? 'Update the display name or choose a different model.'
      : selectingConnection.value || initialConnection.value ? 'Choose a saved provider connection and the model to add.' : 'Connect a provider once, then add models using its saved key.')
function invalidateProbe(name: string, connection = false): void {
  const key = credentialStateKey(name, connection)
  probeGenerations.set(key, (probeGenerations.get(key) || 0) + 1)
  if (testing.value.has(key)) {
    const next = new Set(testing.value)
    next.delete(key)
    testing.value = next
  }
}
async function withCredentialAction<T>(name: string, action: CredentialAction, run: () => Promise<T>, connection = false): Promise<T | undefined> {
  const key = credentialStateKey(name, connection)
  if (credentialIsBusy(name, connection)) return undefined
  credentialActions.value = new Map(credentialActions.value).set(key, action)
  try {
    return await run()
  } finally {
    if (credentialAction(name, connection) === action) {
      const next = new Map(credentialActions.value)
      next.delete(key)
      credentialActions.value = next
    }
  }
}
async function testCredential(name: string): Promise<void> {
  const key = credentialStateKey(name)
  if (credentialIsBusy(name) || testing.value.has(key)) return
  const generation = (probeGenerations.get(key) || 0) + 1
  probeGenerations.set(key, generation)
  const authority = captureAuthority()
  testing.value = new Set(testing.value).add(key)
  try {
    const result = presentModelProbeResult(await authority.api.testCredential(name))
    if (probeGenerations.get(key) !== generation || !authorityIsCurrent(authority)) return
    tested.value = setMap(tested.value, key, result)
    toast(result.ok ? 'ok' : 'error', result.ok ? `${labelForName(name)}: model responded · ${result.latencyMS}ms` : `${labelForName(name)}: ${result.error || 'failed'}`)
  } catch (error) {
    if (probeGenerations.get(key) !== generation || !authorityIsCurrent(authority)) return
    const message = (error as Error).message
    tested.value = setMap(tested.value, key, { ok: false, latencyMS: 0, error: message, technicalDiagnostic: errorTechnicalDiagnostic(error) }); toast('error', `${labelForName(name)}: ${message}`)
  } finally {
    if (probeGenerations.get(key) === generation) { const next = new Set(testing.value); next.delete(key); testing.value = next }
  }
}
async function remove(name: string, connection = false): Promise<void> {
  await withCredentialAction(name, 'deleting', async () => {
    const authority = captureAuthority()
    const ok = await confirmDialog({ title: `Delete credential “${labelForName(name, connection)}”?`, message: 'Agents using it will need reassigning.', danger: true, confirmLabel: 'Delete' })
    if (!ok || !authorityIsCurrent(authority)) return
    invalidateProbe(name, connection)
    const result = await mutate(authority.store, { run: async () => {
      await (connection ? authority.api.deleteProviderConnection(name) : authority.api.deleteCredential(name))
      return true
    }, success: 'Credential deleted.', failure: 'Delete failed', reload: ['credentials'] })
    if (result && authorityIsCurrent(authority)) clearProbeState(name, connection)
  }, connection)
}
function clearTest(name: string, connection = false): void { const next = new Map(tested.value); next.delete(credentialStateKey(name, connection)); tested.value = next }
function clearProbeState(name: string, connection = false): void { clearTest(name, connection) }
function addModel(connectionName: string): void {
  initialConnection.value = connectionName
  editingCredential.value = undefined; editName.value = null; creating.value = true; editorFamily.value = 'chat'; saveError.value = null
}
function continueWithSavedProvider(connectionName: string): void {
  connectedProvider.value = null
  initialConnection.value = connectionName
  selectingConnection.value = true
  editingCredential.value = undefined; editName.value = null; creating.value = true; editorFamily.value = 'chat'; saveError.value = null
  editorGeneration.value++
}
function toggleEdit(credential: Credential): void {
  if (credentialIsBusy(credential.name, providerConnectionCredential(credential))) return
  editingCredential.value = { ...credential }
  editorFamily.value = harnessCredential(credential) ? 'harness' : 'chat'
  editName.value = credential.name
  saveError.value = null
}
function beginModel(): void {
  if (props.routeOwned) emit('navigate', { kind: 'create', resource: 'model', type: 'from-connection' })
  else { selectingConnection.value = true; creating.value = true }
}
function selectCreateAction(id: string): void {
  if (id === 'model') beginModel()
  else if (id === 'provider') openProvider()
  else if (id === 'harness') beginHarness()
}
function beginHarness(): void {
  if (props.routeOwned) {
    emit('navigate', { kind: 'create', resource: 'model', type: 'harness' })
    return
  }
  initialConnection.value = ''
  editingCredential.value = undefined
  editName.value = null
  selectingConnection.value = false
  creating.value = true
  editorFamily.value = 'harness'
  saveError.value = null
  editorGeneration.value++
}
function openProvider(): void {
  const continueToModel = selectingConnection.value
  if (props.routeOwned) {
    emit('connect-provider', continueToModel)
    emit('navigate', { kind: 'create', resource: 'model' })
  }
  else {
    continueAddedProvider.value = continueToModel
    selectingConnection.value = false; initialConnection.value = ''; editingCredential.value = undefined
    editName.value = null; saveError.value = null; editorGeneration.value++; creating.value = true
  }
}
function cancelCreate(): void {
  if (createBusy.value) return
  initialConnection.value = ''
  editingCredential.value = undefined
  editName.value = null
  creating.value = false
  connectedProvider.value = null
  continueAddedProvider.value = false
  savedProviderDiscovery.value = null
  selectingConnection.value = Boolean(props.selectConnection)
  editorFamily.value = 'chat'
  saveError.value = null
  if (props.createRoute) emit('create-cancel', { store: props.store, authorityEpoch: props.authorityEpoch, createSession: props.createSession })
}
async function saveModel(body: CredentialWrite, probe?: CredentialTestResult): Promise<void> {
  if (createBusy.value) return
  const edited = !!editingCredential.value
  const savingProviderConnection = !edited && !body.connectionRef && !body.newConnection && body.model === ''
  if (!edited && credentials.value.data.some(item => providerConnectionCredential(item) === savingProviderConnection && item.name === body.name && (!body.connectionRef || item.connectionRef !== body.connectionRef))) { saveError.value = savingProviderConnection ? 'A provider connection with this name already exists. Choose another name.' : 'A model with this name already exists. Choose another name.'; return }
  const authority = captureAuthority()
  const fence = { store: props.store, authorityEpoch: props.authorityEpoch, createSession: props.createSession }
  createBusy.value = true
  saveError.value = null
  invalidateProbe(body.name, (editingCredential.value ? providerConnectionCredential(editingCredential.value) : false) || savingProviderConnection)
  try {
    let savedConnection: Credential | undefined
    if (body.newConnection) {
      savedConnection = await authority.api.saveCredential(body.newConnection)
      if (!authorityIsCurrent(authority) || fence.createSession !== props.createSession) return
    }
    const { newConnection: _newConnection, ...modelBody } = body
    const result = await authority.api.saveCredential(modelBody)
    if (!authorityIsCurrent(authority) || fence.createSession !== props.createSession) return
    clearProbeState(body.name, Boolean(result.isConnection))
    if (probe) tested.value = setMap(tested.value, credentialStateKey(body.name, Boolean(result.isConnection)), probe)
    if (savingProviderConnection) {
      const acknowledged = { ...result, ...(probe?.models ? { discovered: probe.models } : {}) }
      // Start the refresh before adoption so AppStore's generation fence keeps
      // this acknowledged connection (and discovery result) if its list read
      // settles before the reconciler has published the same state.
      void authority.store.load('credentials')
      authority.store.adopt('credentials', acknowledged)
      if (!authorityIsCurrent(authority) || fence.createSession !== props.createSession) return
      if (probe?.models) savedProviderDiscovery.value = { connectionName: acknowledged.name, models: [...probe.models] }
      toast('ok', 'Provider connected.')
      if (props.continueAddModelAfterProviderConnect || continueAddedProvider.value) continueWithSavedProvider(acknowledged.name)
      else connectedProvider.value = acknowledged
      return
    }
    if (!edited) {
      // A successful create response already contains the saved binding. Do
      // not keep the editor (or its chat recovery handoff) waiting on a list
      // request; reconcile in the background and immediately use the server
      // acknowledgement as the local snapshot.
      void authority.store.load('credentials')
      if (savedConnection) authority.store.adopt('credentials', savedConnection)
      authority.store.adopt('credentials', result)
      if (!authorityIsCurrent(authority) || fence.createSession !== props.createSession) return
      cancelEditorAfterSave()
      toast('ok', isHarnessProvider(body.provider) ? 'Harness identity added.' : 'Model connected.')
      if (props.routeOwned) emit('create-success', { resource: 'model', name: body.name, item: result, ...fence })
      return
    }
    await authority.store.load('credentials')
    if (!authorityIsCurrent(authority) || fence.createSession !== props.createSession) return
    cancelEditorAfterSave()
    toast('ok', edited ? 'Credential updated.' : isHarnessProvider(body.provider) ? 'Harness identity added.' : 'Model connected.')
    if (!edited && props.routeOwned) emit('create-success', { resource: 'model', name: body.name, item: result, ...fence })
  } catch (error) {
    if (authorityIsCurrent(authority) && fence.createSession === props.createSession) {
      saveError.value = (body.newConnection ? 'The provider connection may have been saved, but the model could not be added. Retry to finish connecting. ' : '') + (error as Error).message
      void authority.store.load('credentials')
    }
  } finally { if (authorityIsCurrent(authority) && fence.createSession === props.createSession) createBusy.value = false }
}
async function renameModel(displayName: string): Promise<void> {
  const credential = editingCredential.value
  if (!credential || createBusy.value || credentialIsBusy(credential.name, Boolean(credential.isConnection))) return
  const authority = captureAuthority()
  const fence = { store: props.store, authorityEpoch: props.authorityEpoch, createSession: props.createSession }
  createBusy.value = true
  saveError.value = null
  try {
    const renamed = await withCredentialAction(credential.name, 'saving', () => authority.api.renameCredential(credential, displayName), Boolean(credential.isConnection))
    if (!renamed) return
    // A successful PATCH belongs to its originating store even when this
    // routed view has since unmounted. Retired stores reject both operations;
    // the active-route guard below still owns editor and toast effects.
    if (!authority.store.isRetired()) {
      // Refresh in the background, then adopt the acknowledgement immediately
      // so the edited display name survives list reads that lag the annotation patch.
      void authority.store.load('credentials')
      authority.store.adopt('credentials', renamed)
    }
    if (!authorityIsCurrent(authority) || fence.createSession !== props.createSession) return
    cancelEditorAfterSave()
    toast('ok', credential.isConnection ? 'Provider connection name updated.' : 'Model display name updated.')
  } catch (error) {
    if (authorityIsCurrent(authority) && fence.createSession === props.createSession) {
      saveError.value = (error as Error).message
      void authority.store.load('credentials')
    }
  } finally { if (authorityIsCurrent(authority) && fence.createSession === props.createSession) createBusy.value = false }
}
function cancelEditorAfterSave(): void { initialConnection.value = ''; editingCredential.value = undefined; editName.value = null; creating.value = false; connectedProvider.value = null; continueAddedProvider.value = false; savedProviderDiscovery.value = null; selectingConnection.value = Boolean(props.selectConnection); editorFamily.value = 'chat' }
const usagePanel = ref<InstanceType<typeof AgentUsage> | null>(null)
defineExpose({ loadCatalog, loadUsage: () => usagePanel.value?.loadUsage() })
</script>

<template>
  <div :class="createRoute || creating || editingCredential ? 'k-create-page' : 'k-resource-page k-resource-page--collection agents-panel agents-route-panel agents-models-page'">
    <template v-if="createRoute || creating || editingCredential">
      <button type="button" class="k-btn k-btn--ghost k-back-action" :disabled="editor?.locked" @click="editor?.cancel()"><ArrowLeft :stroke-width="1.75" aria-hidden="true" /> {{ returnLabel }}</button>
      <header class="k-create-header"><h1 class="k-create-title">{{ editorTitle }}</h1><p class="k-create-description">{{ editorDescription }}</p></header>
      <section v-if="connectedProvider" class="k-card agents-provider-connected" aria-labelledby="provider-connected-title">
        <div class="agents-provider-connected__mark"><Check :size="18" :stroke-width="2" aria-hidden="true" /></div>
        <div class="agents-provider-connected__copy"><h2 id="provider-connected-title">Provider connected</h2><p role="status">{{ connectedProvider.displayName || connectedProvider.name }} is saved. Add a model now or later.</p></div>
        <div class="agents-provider-connected__actions"><button type="button" class="k-btn k-btn--primary" @click="continueWithSavedProvider(connectedProvider.name)">Add model</button><button type="button" class="k-btn k-btn--ghost" @click="cancelCreate">Done</button></div>
      </section>
      <ProviderConnectionEditor v-else-if="!editorIsHarness" ref="editor" :key="`provider:${editorGeneration}:${createSession}:${editName || initialConnection || 'new'}`" :api="api" :credentials="credentials.data" :credential="editingCredential" :initial-connection="initialConnection" :select-connection="selectingConnection" :discovery-fallback="savedProviderDiscovery" :busy="createBusy" :error="saveError" @save="saveModel" @rename="renameModel" @cancel="cancelCreate" @connect-provider="openProvider" />
      <ModelConnectionEditor v-else ref="editor" :key="`${editorGeneration}:${createSession}:${editName || 'new'}`" :api="api" :credential="editingCredential" :initial-family="editorFamily" :busy="createBusy" :error="saveError" :catalog="catalog" @save="saveModel" @cancel="cancelCreate" @family-change="editorFamily = $event" />
    </template>
    <template v-else>
      <header class="k-resource-page__header">
        <div class="k-resource-page__heading">
          <component :is="routeOwned ? 'h2' : 'h3'" id="models-title" class="k-resource-page__title">Models</component>
          <p class="k-resource-page__subtitle">{{ setupNavigation ? 'Manage the models and provider access your agents use.' : 'Connect and manage models for your agents.' }}</p>
        </div>
        <div v-if="setupNavigation" class="k-resource-page__actions">
          <ActionMenu label="Create" primary-menu :items="createActions" @select="selectCreateAction" />
        </div>
      </header>
      <nav v-if="!setupNavigation" class="k-tabs k-tabs--compact agents-models-tabs" aria-label="Model management">
        <a v-for="tab in MODELS_TABS" :key="tab.id" class="k-tab agents-models-tab" :href="hashFor(tabRoute(tab.id))" :aria-current="activeTab === tab.id ? 'page' : undefined" @click="navigateTab($event, tab.id)">{{ tab.label }}</a>
      </nav>
      <template v-if="credentialView">
      <template v-if="modelView">
      <div v-if="modelView && catalogError && !catalogHasSnapshot" class="k-error" role="alert">Model catalog unavailable: {{ catalogError }} <button class="k-btn k-btn--ghost" :disabled="catalogLoading" @click="loadCatalog">Retry</button></div>
      <div v-else-if="modelView && catalogError" class="k-stale" role="status">Could not refresh the model catalog. Showing the last loaded catalog. {{ catalogError }} <button class="k-btn k-btn--ghost" :disabled="catalogLoading" @click="loadCatalog">Retry</button></div>
      <div v-else-if="modelView && catalogLoading && !catalogHasSnapshot" class="k-loading-reveal muted" role="status">Loading model catalog…</div>
      <header class="k-resource-page__header" aria-labelledby="available-models-title">
        <div class="k-resource-page__heading"><h3 id="available-models-title" class="k-resource-page__title">Available models</h3><p class="k-resource-page__subtitle">Models you can assign to agents.</p></div>
        <div v-if="!setupNavigation" class="k-resource-page__header-side">
          <div class="k-resource-page__actions">
            <button v-if="!showFirstRun" type="button" class="k-btn k-btn--primary" @click="beginModel"><Plus :stroke-width="1.75" aria-hidden="true" /> Add model</button>
          </div>
        </div>
      </header>
      <FirstRunGuide v-if="modelView && showFirstRun" title="Connect a provider to add models" description="Bring an API key to connect a provider once, then reuse it across models for your agents." primary-label="Connect provider" :steps="[{ label: 'Provider', description: 'Connect a provider and choose a model' }, { label: 'Agent', description: 'Assign it to an agent' }, { label: 'Run', description: 'Start a conversation' }]" :current-step="0" journey-label="Model setup path" @primary="openProvider" />
      <div v-else-if="!setupNavigation && !credentials.loaded && !credentials.error" class="k-loading-reveal muted" role="status">Loading credentials…</div>
      <div v-if="credentials.error" :class="credentials.hasSnapshot ? 'k-stale' : 'k-error'" :role="credentials.hasSnapshot ? 'status' : 'alert'">
        <span v-if="setupNavigation && credentials.hasSnapshot">Showing the last successful results. </span>{{ credentials.error }}
        <button type="button" class="k-btn k-btn--ghost" :disabled="credentials.loading" @click="store.load('credentials')">{{ credentials.loading ? 'Retrying…' : 'Retry' }}</button>
      </div>
      </template>
      <ModelCollection v-if="modelView && credentials.hasSnapshot && modelRows.length" :rows="modelRows" preference-key="railgrid:agents:models:layout" :table-only="setupNavigation" :show-provider-connection="setupNavigation">
        <template v-if="setupNavigation" #name-prefix="{ row }">
          <ResourceTableExpandButton
            :expanded="expandedModel === row.id"
            :label="`Show details for ${row.name}`"
            :expanded-label="`Hide details for ${row.name}`"
            :controls="`${modelDetailsPrefix}-${row.id}`"
            @click="toggleModelDetails(row.id)"
          />
        </template>
        <template #cards="{ rows }">
        <ModelConnectionCard v-for="{ credential } in rows" :key="credential.name" :name="credential.displayName || credential.name" :model="credential.model || '—'" :endpoint="credential.baseURL || 'Provider default'" endpoint-label="Endpoint" resource-label="Model" :configured="credential.secretResolved !== false" :busy="credentialIsBusy(credential.name)"
          :test-state="testing.has(credentialStateKey(credential.name)) ? 'Testing…' : tested.get(credentialStateKey(credential.name))?.ok ? `Test passed · ${tested.get(credentialStateKey(credential.name))?.latencyMS} ms` : tested.has(credentialStateKey(credential.name)) ? 'Test failed' : statusLabel(credential)"
          :test-tone="tested.get(credentialStateKey(credential.name))?.ok ? 'success' : tested.has(credentialStateKey(credential.name)) ? 'danger' : statusTone(credential)">
          <p v-if="credential.connectionRef" class="agents-hint">Connection: {{ credential.connectionName || credential.connectionRef }}</p>
          <ModelDetails
            :model="lookupModel(credential.model || '')"
            :catalog-fallback="missingCatalogLabel()"
            :primary-agents="primaryOf(credential)"
            :fallback-agents="fallbackOf(credential)"
          />
          <p v-if="tested.get(credentialStateKey(credential.name))?.error" class="k-error" role="alert">{{ tested.get(credentialStateKey(credential.name))?.error }}</p>
          <p v-else-if="credential.ready === false && credential.statusMessage" class="k-error" role="alert">The model connection needs attention. Edit it to review the endpoint and credential.</p>
          <TechnicalDetails :diagnostic="tested.get(credentialStateKey(credential.name))?.technicalDiagnostic || (credential.ready === false ? credential.statusMessage : '')" />
          <p v-if="credential.discovered?.length" class="agents-hint">{{ credential.discovered.length }} chat model{{ credential.discovered.length === 1 ? '' : 's' }} available from this endpoint</p>
          <template #actions><button class="k-btn k-btn--ghost" :disabled="credentialIsBusy(credential.name)" @click="toggleEdit(credential)">Edit</button><button v-if="credential.model?.trim()" class="k-btn k-btn--ghost" :disabled="testing.has(credentialStateKey(credential.name)) || credentialIsBusy(credential.name)" @click="testCredential(credential.name)">Test connection</button><button class="k-icon-action" :disabled="credentialIsBusy(credential.name)" :aria-busy="credentialAction(credential.name) === 'deleting'" :title="`Delete ${credentialLabel(credential)}`" :aria-label="credentialAction(credential.name) === 'deleting' ? `Deleting ${credentialLabel(credential)}…` : `Delete ${credentialLabel(credential)}`" @click="remove(credential.name)"><Trash2 :stroke-width="1.75" aria-hidden="true" /></button></template>
        </ModelConnectionCard>
        </template>
        <template #details="{ row: { credential } }">
          <p v-if="tested.get(credentialStateKey(credential.name))?.error" class="k-error" role="alert">{{ tested.get(credentialStateKey(credential.name))?.error }}</p>
          <TechnicalDetails :diagnostic="tested.get(credentialStateKey(credential.name))?.technicalDiagnostic || (credential.ready === false ? credential.statusMessage : '')" />
        </template>
        <template #actions="{ row: { credential } }">
          <ResourceTableEditButton :label="`Edit ${credentialLabel(credential)}`" :disabled="credentialIsBusy(credential.name)" @click="toggleEdit(credential)" />
          <ResourceTableActionButton v-if="credential.model?.trim()" :icon="Play" :label="`Test connection for ${credentialLabel(credential)}`" :busy="testing.has(credentialStateKey(credential.name))" :disabled="credentialIsBusy(credential.name)" @click="testCredential(credential.name)" />
          <ResourceTableDeleteButton :label="`Delete ${credentialLabel(credential)}`" :busy="credentialAction(credential.name) === 'deleting'" :disabled="credentialIsBusy(credential.name)" @click="remove(credential.name)" />
        </template>
        <template v-if="setupNavigation" #after-row="{ row, columnCount }">
          <tr v-if="expandedModel === row.id" :id="`${modelDetailsPrefix}-${row.id}`">
            <td :colspan="columnCount">
              <div class="agents-model-details">
                <p v-if="row.credential.baseURL" class="agents-hint">Endpoint: {{ row.credential.baseURL }}</p>
                <ModelDetails
                  :model="lookupModel(row.model)"
                  :catalog-fallback="missingCatalogLabel()"
                  :primary-agents="primaryOf(row.credential)"
                  :fallback-agents="fallbackOf(row.credential)"
                />
              </div>
            </td>
          </tr>
        </template>
      </ModelCollection>
      <div v-if="modelView && credentials.hasSnapshot && !showFirstRun && !modelRows.length" class="k-card agents-models-empty"><h3>No models configured</h3><p>Add a model using an existing provider connection, or connect a new provider.</p></div>
      <section v-if="connectionView" class="k-resource-page k-resource-page--collection" :class="{ 'agents-model-providers': setupNavigation }" aria-labelledby="provider-connections-title">
        <header class="k-resource-page__header">
          <div class="k-resource-page__heading">
            <h3 id="provider-connections-title" class="k-resource-page__title">{{ setupNavigation ? 'Provider connections & harness identities' : 'Provider connections' }}</h3>
            <p class="k-resource-page__subtitle">Shared API access for models and logins for coding harnesses.</p>
          </div>
          <div v-if="!setupNavigation" class="k-resource-page__header-side">
            <div class="k-resource-page__actions">
              <button type="button" class="k-btn k-btn--ghost secondary" @click="beginHarness"><Plus :stroke-width="1.75" aria-hidden="true" /> Add harness identity</button>
              <button type="button" class="k-btn k-btn--primary" @click="openProvider"><Plus :stroke-width="1.75" aria-hidden="true" /> Connect provider</button>
            </div>
          </div>
        </header>
        <div v-if="!setupNavigation && showProviderFirstRun && credentials.error" class="k-stale" role="status">{{ credentials.error }} <button class="k-btn k-btn--ghost" @click="store.load('credentials')">Retry</button></div>
        <FirstRunGuide v-if="!setupNavigation && showProviderFirstRun" title="Add provider access or a harness identity" description="Connect an API provider to add chat models, or add a Claude Code or Codex login for an agent running on an edge machine." primary-label="Connect provider" :actions-visible="false" :steps="[{ label: 'Provider', description: 'Connect a provider and choose a model' }, { label: 'Agent', description: 'Assign it to an agent' }, { label: 'Run', description: 'Start a conversation' }]" :current-step="0" journey-label="Model setup path" />
        <ResourceTable
          v-else-if="!setupNavigation || !credentials.error || credentials.hasSnapshot"
          :columns="[{ key: 'name', label: 'Name', primary: true, sortable: true }, { key: 'type', label: 'Type', sortable: true }, ...(setupNavigation ? [] : [{ key: 'endpoint', label: 'Endpoint / machine', sortable: true }]), { key: 'modelCount', label: 'Models', sortable: true, sortValue: connectionModelCountSortValue }, { key: 'status', label: 'Readiness', sortable: true }, { key: 'actions', label: '', ariaLabel: 'Actions', align: 'end' }]"
          :rows="connectionRows"
          row-key="id"
          aria-label="Provider connections and harness identities"
          :loaded="credentials.hasSnapshot"
          :loading="credentials.loading"
          :error="setupNavigation ? null : credentials.error"
          :stale="!setupNavigation && credentials.hasSnapshot && !!credentials.error"
          :retryable="!setupNavigation"
          searchable
          search-placeholder="Search provider access and identities…"
          :search-keys="['name', 'type', 'endpoint']"
          empty-text="No provider connections or harness identities yet."
          search-empty-text="No provider connections or harness identities match your search."
          paginated
          :interactive="false"
          @retry="store.load('credentials')"
        >
          <template #actions="{ row }">
            <ResourceTableActionButton v-if="providerConnectionCredential(connectionCredential(row))" :icon="Plus" :label="`Add model to ${row.name}`" :disabled="credentialIsBusy(connectionCredential(row).name, true)" @click="addModel(connectionCredential(row).name)" />
            <ResourceTableEditButton :label="providerConnectionCredential(connectionCredential(row)) ? `Edit provider connection ${row.name}` : `Edit ${row.name} harness identity`" :disabled="credentialIsBusy(connectionCredential(row).name, providerConnectionCredential(connectionCredential(row)))" @click="toggleEdit(connectionCredential(row))" />
            <ResourceTableDeleteButton :label="providerConnectionCredential(connectionCredential(row)) ? `Delete provider connection ${row.name}` : `Delete ${row.name} harness identity`" :busy-label="providerConnectionCredential(connectionCredential(row)) ? `Deleting provider connection ${row.name}…` : `Deleting ${row.name} harness identity…`" :busy="credentialAction(connectionCredential(row).name, providerConnectionCredential(connectionCredential(row))) === 'deleting'" :disabled="credentialIsBusy(connectionCredential(row).name, providerConnectionCredential(connectionCredential(row)))" @click="remove(connectionCredential(row).name, providerConnectionCredential(connectionCredential(row)))" />
          </template>
          <template #status="{ row }">
            <StatusBadge :status="String(row.status)" :tone="row.statusTone as 'success' | 'danger' | 'muted'" />
            <p v-if="harnessCredential(connectionCredential(row))" class="agents-hint">The credential check validates login format; a successful first run confirms edge access.</p>
            <TechnicalDetails :diagnostic="connectionCredential(row).statusMessage" />
          </template>
        </ResourceTable>
      </section>
      </template>
      <WorkspaceAssistantCard v-if="!setupNavigation && activeTab === 'workspace-assistant' && overlayControl" :store="store" :overlay-control="overlayControl" />
      <div v-else-if="!setupNavigation && activeTab === 'workspace-assistant'" class="k-card agents-models-empty"><h3>Workspace assistant unavailable</h3><p>Open Agents in the workspace portal to configure your assistant.</p></div>
      <AgentUsage v-if="!setupNavigation && activeTab === 'usage'" ref="usagePanel" :store="store" :api="api" />
    </template>
  </div>
</template>

<style scoped>
.agents-provider-connected {
  display: grid;
  grid-template-columns: 36px minmax(0, 1fr);
  align-items: start;
  gap: 12px 16px;
  width: min(100%, 760px);
  padding: 24px;
}
.agents-provider-connected__mark {
  display: grid;
  width: 36px;
  height: 36px;
  place-items: center;
  border-radius: 50%;
  color: var(--color-success, #2fd6a0);
  background: color-mix(in srgb, var(--color-success, #2fd6a0) 14%, transparent);
}
.agents-provider-connected__copy h2 {
  margin: 1px 0 6px;
  font-size: 16px;
  font-weight: 650;
}
.agents-provider-connected__copy p {
  margin: 0;
  color: var(--color-text-secondary);
}
.agents-provider-connected__actions {
  grid-column: 2;
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
@media (max-width: 560px) {
  .agents-provider-connected { grid-template-columns: 28px minmax(0, 1fr); padding: 18px; }
  .agents-provider-connected__mark { width: 28px; height: 28px; }
  .agents-provider-connected__actions { grid-column: 1 / -1; }
}
</style>
