<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ArrowLeft, Database, LoaderCircle, X } from 'lucide-vue-next'
import { api } from './api'
import LazyCheckboxTree from './LazyCheckboxTree.vue'
import {
  REGISTRATION_LIMIT,
  appendTreePage,
  emptyRegistrationTree,
  exhaustBranchSelection,
  isLeaf,
  loadedSelectableLeaves,
  updateLeafSelection,
  type RegistrationTreeNode,
  type TreePage,
} from './registrationTree'
import {
  materializeRegistrationResults,
  mergeRegistrationResults,
  retryableRegistrationIndices,
  snapshotDiscoveryCoordinates,
  suggestedRegistrationName,
  summarizeRegistration,
} from './registrationFlow'
import { resourceNameError } from './resourceName'
import type {
  DiscoveryCoordinates,
  InitializationResource,
  InitializationState,
  RegistrationItem,
  RegistrationResult,
  RemoteCatalog,
  RemoteSchema,
  RemoteTable,
  RemoteWarehouse,
} from './registrationTypes'
import type { Connection, ErrorResponse, Table, Warehouse } from './types'

type Kind = 'warehouse' | 'table'
type Step = 'source' | 'browse' | 'review' | 'results'

interface ReviewEntry { key: string; label: string; item: RegistrationItem; name: string }

const props = defineProps<{ kind: Kind }>()
const emit = defineEmits<{
  (event: 'close'): void
  (event: 'registered'): void
  (event: 'navigate', path: 'connections' | 'warehouses'): void
}>()

const root = ref<HTMLElement | null>(null)
const step = ref<Step>('source')
const connections = ref<Connection[]>([])
const warehouses = ref<Warehouse[]>([])
const currentTables = ref<Table[]>([])
const connectionRef = ref('')
const warehouseRef = ref('')
const tree = ref(emptyRegistrationTree())
const rootLoading = ref(false)
const rootError = ref('')
const rootNextPageToken = ref<string>()
const branchChecking = ref(false)
const results = ref<RegistrationResult[]>([])
const reviewEntries = ref<ReviewEntry[]>([])
const registrationItems = ref<RegistrationItem[]>([])
const reviewCoordinates = ref<DiscoveryCoordinates | null>(null)
const submitting = ref(false)
const registrationFrozen = ref(false)
const error = ref<string | null>(null)
const initializationState = reactive<InitializationState>({ connections: 'idle', warehouses: 'idle', tables: 'idle' })
const initializationErrors = reactive<Record<InitializationResource, string | null>>({ connections: null, warehouses: null, tables: null })
const initializationGeneration: Record<InitializationResource, number> = { connections: 0, warehouses: 0, tables: 0 }
let treeGeneration = 0
let previousFocus: HTMLElement | null = null

const plural = computed(() => props.kind === 'warehouse' ? 'warehouses' : 'tables')
const matchingWarehouses = computed(() => warehouses.value.filter(item => item.connectionRef === connectionRef.value))
const selectedNodes = computed(() => tree.value.selectedLeafIds.map(id => tree.value.nodes[id]).filter((node): node is RegistrationTreeNode => !!node && isLeaf(node)))
const requiredInitialization = computed<readonly InitializationResource[]>(() => props.kind === 'table' ? ['connections', 'warehouses', 'tables'] : ['connections', 'warehouses'])
const initializationPending = computed(() => requiredInitialization.value.some(resource => initializationState[resource] === 'loading'))
const dialogBusy = computed(() => rootLoading.value || branchChecking.value || submitting.value || initializationPending.value || Object.values(tree.value.nodes).some(node => node.loading))
const failedResultIndices = computed(() => retryableRegistrationIndices(results.value))
const retryableResults = computed(() => failedResultIndices.value.filter(index => !!registrationItems.value[index]))
const stepLabel = computed(() => ({ source: 'Choose source', browse: `Browse ${plural.value}`, review: 'Review', results: 'Results' })[step.value])

function message(cause: unknown): string {
  if (cause && typeof cause === 'object') {
    const failure = cause as Partial<ErrorResponse>
    if (failure.reason && failure.message) return `${failure.reason}: ${failure.message}`
    if (failure.message) return failure.message
  }
  return cause instanceof Error ? cause.message : String(cause)
}

function phaseLabel(resource: InitializationResource): string {
  return resource === 'connections' ? 'connections' : resource === 'warehouses' ? 'registered warehouses' : 'registered tables'
}

async function loadInitializationPhase(resource: InitializationResource): Promise<void> {
  const generation = ++initializationGeneration[resource]
  initializationState[resource] = 'loading'; initializationErrors[resource] = null
  try {
    if (resource === 'connections') {
      const list = await api.listConnections(); if (initializationGeneration[resource] !== generation) return
      connections.value = list
      if (!list.some(item => item.name === connectionRef.value)) connectionRef.value = list[0]?.name ?? ''
    } else if (resource === 'warehouses') {
      const list = await api.listWarehouses(); if (initializationGeneration[resource] !== generation) return
      warehouses.value = list
      if (!list.some(item => item.name === warehouseRef.value && item.connectionRef === connectionRef.value)) warehouseRef.value = list.find(item => item.connectionRef === connectionRef.value)?.name ?? ''
    } else {
      const list = await api.listTables(); if (initializationGeneration[resource] !== generation) return
      currentTables.value = list
    }
    initializationState[resource] = 'success'
  } catch (cause) {
    if (initializationGeneration[resource] !== generation) return
    initializationState[resource] = 'error'; initializationErrors[resource] = message(cause)
  }
}

async function initialize(): Promise<void> { await Promise.all(requiredInitialization.value.map(loadInitializationPhase)) }
function retryInitialization(resource: InitializationResource): void { if (initializationState[resource] !== 'loading') void loadInitializationPhase(resource) }

function initializationBlocker(): string | null {
  const pending = requiredInitialization.value.find(resource => initializationState[resource] === 'loading')
  if (pending) return `Loading ${phaseLabel(pending)}. Try again when loading finishes.`
  const failed = requiredInitialization.value.find(resource => initializationState[resource] === 'error')
  return failed ? `${phaseLabel(failed)} could not be loaded. Retry that phase before continuing.` : null
}

function coordinates(): DiscoveryCoordinates {
  return snapshotDiscoveryCoordinates({ connectionRef: connectionRef.value, warehouseRef: warehouseRef.value })
}

function resetTree(): void {
  treeGeneration += 1
  tree.value = emptyRegistrationTree()
  rootLoading.value = false; rootError.value = ''; rootNextPageToken.value = undefined; branchChecking.value = false
}

function warehouseNode(item: RemoteWarehouse): RegistrationTreeNode {
  const existing = warehouses.value.some(current => current.connectionRef === connectionRef.value && current.warehouseID === item.id)
  const disabled = item.supported === false || item.unsupported === true || existing
  return { id: `warehouse:${item.id}`, kind: 'warehouse', label: item.name, detail: `ID: ${item.id} · ${item.state || 'state unavailable'}`, depth: 1, disabled, disabledReason: existing ? 'Already registered' : disabled ? item.unsupportedReason || item.reason || 'Unsupported warehouse' : undefined, expanded: false, childrenLoaded: true, loading: false, childIds: [], registration: { warehouseID: item.id } }
}

function catalogNode(item: RemoteCatalog): RegistrationTreeNode {
  const disabled = item.supported === false
  return { id: `catalog:${item.name}`, kind: 'catalog', label: item.name, detail: item.comment || item.catalogType || 'Unity Catalog', depth: 1, disabled, disabledReason: disabled ? item.unsupportedReason || item.reason || 'Unsupported catalog' : undefined, expanded: false, childrenLoaded: false, loading: false, childIds: [], catalog: item.name }
}

function schemaNode(item: RemoteSchema, parentId: string): RegistrationTreeNode {
  const disabled = item.supported === false
  return { id: `schema:${item.catalog}:${item.name}`, kind: 'schema', label: item.name, detail: item.comment || item.catalog, parentId, depth: 2, disabled, disabledReason: disabled ? item.unsupportedReason || item.reason || 'Unsupported schema' : undefined, expanded: false, childrenLoaded: false, loading: false, childIds: [], catalog: item.catalog, schema: item.name }
}

function tableNode(item: RemoteTable, parentId: string): RegistrationTreeNode {
  const existing = currentTables.value.some(current => current.connectionRef === connectionRef.value && current.catalog === item.catalog && current.schema === item.schema && current.table === item.name)
  const disabled = item.supported === false || item.unsupported === true || existing
  return { id: `table:${item.catalog}:${item.schema}:${item.name}`, kind: 'table', label: item.name, detail: item.tableType || item.dataSourceFormat || 'Table', parentId, depth: 3, disabled, disabledReason: existing ? 'Already registered' : disabled ? item.unsupportedReason || item.reason || 'Unsupported table' : undefined, expanded: false, childrenLoaded: true, loading: false, childIds: [], catalog: item.catalog, schema: item.schema, table: item.name }
}

async function fetchChildren(parent: RegistrationTreeNode, pageToken?: string): Promise<TreePage> {
  if (parent.kind === 'catalog') {
    const page = await api.discoverSchemas(connectionRef.value, parent.catalog || parent.label, pageToken)
    return { nodes: page.items.map(item => schemaNode(item, parent.id)), nextPageToken: page.nextPageToken }
  }
  if (parent.kind === 'schema') {
    const page = await api.discoverTables(connectionRef.value, parent.catalog || '', parent.schema || parent.label, pageToken)
    return { nodes: page.items.map(item => tableNode(item, parent.id)), nextPageToken: page.nextPageToken }
  }
  return { nodes: [] }
}

async function loadRoot(append = false): Promise<void> {
  if (rootLoading.value) return
  const generation = treeGeneration
  const token = append ? rootNextPageToken.value : undefined
  rootLoading.value = true; rootError.value = ''
  try {
    const page = props.kind === 'warehouse' ? await api.discoverWarehouses(connectionRef.value, token) : await api.discoverCatalogs(connectionRef.value, token)
    if (generation !== treeGeneration) return
    const nodes = props.kind === 'warehouse' ? (page.items as RemoteWarehouse[]).map(warehouseNode) : (page.items as RemoteCatalog[]).map(catalogNode)
    if (!append) tree.value = emptyRegistrationTree()
    appendTreePage(tree.value, undefined, { nodes })
    rootNextPageToken.value = page.nextPageToken
  } catch (cause) { if (generation === treeGeneration) rootError.value = message(cause) }
  finally { if (generation === treeGeneration) rootLoading.value = false }
}

async function loadNodePage(id: string, append = false): Promise<void> {
  const node = tree.value.nodes[id]
  if (!node || node.loading || isLeaf(node)) return
  const generation = treeGeneration
  node.loading = true; node.error = undefined
  try {
    const page = await fetchChildren(node, append ? node.nextPageToken : undefined)
    if (generation !== treeGeneration || tree.value.nodes[id] !== node) return
    appendTreePage(tree.value, id, page)
  } catch (cause) { if (generation === treeGeneration) node.error = message(cause) }
  finally { if (generation === treeGeneration && tree.value.nodes[id] === node) node.loading = false }
}

async function expandNode(id: string): Promise<void> {
  const node = tree.value.nodes[id]
  if (!node || node.disabled || isLeaf(node)) return
  if (node.error) { node.expanded = true; await loadNodePage(id); return }
  node.expanded = !node.expanded
  if (node.expanded && !node.childrenLoaded) await loadNodePage(id)
}

async function toggleNode(id: string, checked: boolean): Promise<void> {
  const node = tree.value.nodes[id]
  if (!node || node.disabled || submitting.value || branchChecking.value) return
  error.value = null
  if (isLeaf(node)) { error.value = updateLeafSelection(tree.value, id, checked); return }
  if (!checked) {
    const descendants = new Set(loadedSelectableLeaves(tree.value, id))
    tree.value.selectedLeafIds = tree.value.selectedLeafIds.filter(selected => !descendants.has(selected))
    return
  }
  const generation = treeGeneration
  const remaining = REGISTRATION_LIMIT - tree.value.selectedLeafIds.length
  branchChecking.value = true
  const result = await exhaustBranchSelection(tree.value, id, async (parent, token) => {
    const page = await fetchChildren(parent, token)
    if (generation !== treeGeneration) throw new Error('Connection changed while selecting the branch; nothing was selected.')
    return page
  }, remaining)
  if (generation === treeGeneration) {
    if (result.complete && result.state) { tree.value = result.state; tree.value.nodes[id].expanded = true }
    else error.value = result.reason || 'The complete branch could not be selected.'
    branchChecking.value = false
  }
}

function loadMore(parentId?: string): void { if (parentId) void loadNodePage(parentId, true); else void loadRoot(true) }

async function fromSource(): Promise<void> {
  error.value = null
  const blocker = initializationBlocker(); if (blocker) { error.value = blocker; return }
  if (!connectionRef.value) { error.value = 'Select a connection.'; return }
  if (props.kind === 'table' && !warehouseRef.value) { error.value = 'Select a registered warehouse on this connection.'; return }
  resetTree(); step.value = 'browse'; await loadRoot()
  focusStep()
}

function registrationItem(node: RegistrationTreeNode, name: string): RegistrationItem {
  return node.kind === 'warehouse' ? { name, warehouseID: node.registration?.warehouseID } : { name, catalog: node.catalog, schema: node.schema, table: node.table }
}

function suggested(node: RegistrationTreeNode): string { return node.kind === 'warehouse' ? suggestedRegistrationName(node.label) : suggestedRegistrationName(node.catalog || '', node.schema || '', node.table || node.label) }

function validateReviewEntries(entries: readonly ReviewEntry[]): string | null {
  if (!entries.length) return `Select at least one ${props.kind}.`
  for (const entry of entries) { const invalid = resourceNameError(entry.name, 'Resource name'); if (invalid) return invalid }
  return new Set(entries.map(entry => entry.name)).size === entries.length ? null : 'Each selected resource needs a unique Faros resource name.'
}
const reviewValidationError = computed(() => validateReviewEntries(reviewEntries.value))

function review(): void {
  const entries = selectedNodes.value.map(node => { const name = suggested(node); return { key: node.id, label: node.kind === 'warehouse' ? node.label : `${node.catalog}.${node.schema}.${node.table}`, item: registrationItem(node, name), name } })
  const invalid = validateReviewEntries(entries); if (invalid) { error.value = invalid; return }
  reviewEntries.value = entries; reviewCoordinates.value = coordinates(); registrationFrozen.value = false; error.value = null; step.value = 'review'
  focusStep()
}

function emitRegisteredIfNeeded(batch: readonly RegistrationResult[]): void { if (batch.some(result => result.state === 'created' || result.state === 'existing')) emit('registered') }

async function register(): Promise<void> {
  if (submitting.value) return
  const invalid = reviewValidationError.value; if (invalid) { error.value = invalid; return }
  const coordinatesSnapshot = reviewCoordinates.value ?? coordinates()
  registrationItems.value = reviewEntries.value.map(entry => ({ ...entry.item, name: entry.name }))
  registrationFrozen.value = true; submitting.value = true; error.value = null
  try {
    const response = await api.registerResources({ kind: props.kind, connectionRef: coordinatesSnapshot.connectionRef, warehouseRef: props.kind === 'table' ? coordinatesSnapshot.warehouseRef : undefined, items: registrationItems.value })
    results.value = materializeRegistrationResults(registrationItems.value, response.results); step.value = 'results'; emitRegisteredIfNeeded(response.results); focusStep()
    if (response.results.length !== registrationItems.value.length) error.value = `Registration returned ${response.results.length} of ${registrationItems.value.length} expected results.`
  } catch (cause) { results.value = materializeRegistrationResults(registrationItems.value, []); step.value = 'results'; error.value = message(cause); focusStep() }
  finally { submitting.value = false }
}

async function retryFailed(): Promise<void> {
  if (submitting.value || !reviewCoordinates.value) return
  const entries = retryableResults.value.map(index => ({ index, item: registrationItems.value[index] })).filter((entry): entry is { index: number; item: RegistrationItem } => !!entry.item)
  if (!entries.length) return
  submitting.value = true; error.value = null
  try {
    const response = await api.registerResources({ kind: props.kind, connectionRef: reviewCoordinates.value.connectionRef, warehouseRef: props.kind === 'table' ? reviewCoordinates.value.warehouseRef : undefined, items: entries.map(entry => entry.item) })
    results.value = mergeRegistrationResults(results.value, entries.map(entry => entry.item), response.results, entries.map(entry => entry.index)); emitRegisteredIfNeeded(response.results)
  } catch (cause) { error.value = message(cause) }
  finally { submitting.value = false }
}

function back(): void {
  if (dialogBusy.value) return
  error.value = null
  if (step.value === 'browse') { resetTree(); step.value = 'source' }
  else if (step.value === 'review') { registrationFrozen.value = false; step.value = 'browse' }
  focusStep()
}
function navigateTo(path: 'connections' | 'warehouses'): void { previousFocus = null; emit('navigate', path) }
function focusStep(): void { void nextTick(() => root.value?.querySelector<HTMLElement>('.import-body button:not(:disabled),.import-body input:not(:disabled),.import-body select:not(:disabled),[role="treeitem"]')?.focus()) }
function focusDialog(): void { void nextTick(() => root.value?.querySelector<HTMLElement>('.import-head button:not(:disabled),.import-body button:not(:disabled),.import-body input:not(:disabled),.import-body select:not(:disabled),[role="treeitem"]')?.focus()) }
function restoreFocus(): void { const target = previousFocus; previousFocus = null; if (target) void nextTick(() => { if (target.isConnected) target.focus() }) }
function close(): void { if (!submitting.value) { restoreFocus(); emit('close') } }
function dialogTabStops(): HTMLElement[] {
  if (!root.value) return []
  return [...root.value.querySelectorAll<HTMLElement>('button:not(:disabled),input:not(:disabled),select:not(:disabled),[role="treeitem"][tabindex="0"]')]
    .filter(element => element.tabIndex === 0)
}
function keydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') { event.preventDefault(); close(); return }
  if (event.key !== 'Tab' || !root.value) return
  const focusable = dialogTabStops()
  const first = focusable[0], last = focusable.at(-1); if (!first || !last) return
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
}

watch(connectionRef, (next, previous) => { if (next !== previous) { resetTree(); if (step.value !== 'source') step.value = 'source'; if (!matchingWarehouses.value.some(item => item.name === warehouseRef.value)) warehouseRef.value = matchingWarehouses.value[0]?.name ?? '' } })
watch(warehouseRef, (next, previous) => { if (next !== previous) { resetTree(); if (step.value !== 'source') step.value = 'source' } })
onMounted(() => { previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null; focusDialog(); void initialize().then(() => focusStep()) })
onBeforeUnmount(restoreFocus)
</script>

<template>
  <div class="import-backdrop" @pointerdown.self="close">
    <section ref="root" class="import-dialog" role="dialog" aria-modal="true" aria-labelledby="registration-title" aria-describedby="registration-description" :aria-busy="dialogBusy ? 'true' : 'false'" @keydown="keydown">
      <header class="import-head"><div><span class="import-eyebrow">{{ stepLabel }}</span><h2 id="registration-title">New {{ kind }}</h2><p id="registration-description">Browse Databricks metadata and register selected {{ plural }}.</p></div><button class="icon-button" type="button" aria-label="Close" :disabled="submitting" @click="close"><X :stroke-width="1.75" /></button></header>
      <ol class="import-steps" aria-label="Import progress"><li :aria-current="step === 'source' ? 'step' : undefined">Source</li><li :aria-current="step === 'browse' ? 'step' : undefined">Browse</li><li :aria-current="step === 'review' ? 'step' : undefined">Review</li><li :aria-current="step === 'results' ? 'step' : undefined">Results</li></ol>
      <div class="import-body">
        <div v-if="step === 'source'" class="import-stack">
          <label class="field"><span class="field-label">Connection</span><select v-model="connectionRef" :disabled="initializationPending || submitting"><option v-for="item in connections" :key="item.name" :value="item.name">{{ item.name }}</option></select><span class="field-hint">Discovery uses this connection's Databricks credentials.</span></label>
          <label v-if="kind === 'table'" class="field"><span class="field-label">Query warehouse</span><select v-model="warehouseRef" :disabled="initializationPending || submitting"><option value="" disabled>Select warehouse</option><option v-for="item in matchingWarehouses" :key="item.name" :value="item.name">{{ item.name }}</option></select><span class="field-hint">Imported tables remain bound to this same-connection warehouse.</span></label>
          <div class="initialization-status" :aria-busy="initializationPending ? 'true' : 'false'" aria-live="polite"><p v-for="resource in requiredInitialization" :key="resource" class="initialization-phase"><span v-if="initializationState[resource] === 'loading'">Loading {{ phaseLabel(resource) }}…</span><span v-else-if="initializationState[resource] === 'error'" class="error" role="alert">{{ phaseLabel(resource) }} could not be loaded: {{ initializationErrors[resource] }}.</span><span v-else-if="initializationState[resource] === 'success'" class="muted">{{ phaseLabel(resource) }} loaded.</span><button v-if="initializationState[resource] === 'error'" class="link" type="button" @click="retryInitialization(resource)">Retry</button></p></div>
          <div v-if="!connections.length && !initializationPending && initializationState.connections === 'success'" class="prerequisite">A connection is required. <button class="link" type="button" @click="navigateTo('connections')">Go to connections</button></div>
          <div v-else-if="kind === 'table' && !warehouses.length && !initializationPending && initializationState.warehouses === 'success'" class="prerequisite">A registered warehouse is required. <button class="link" type="button" @click="navigateTo('warehouses')">Go to warehouses</button></div>
        </div>
        <LazyCheckboxTree v-else-if="step === 'browse'" :tree="tree" :label="`Available ${plural}`" :root-loading="rootLoading" :root-error="rootError" :root-next-page-token="rootNextPageToken" :busy="branchChecking || submitting" @expand="expandNode" @toggle="toggleNode" @load-more="loadMore" />
        <div v-else-if="step === 'review'" class="review-list" :aria-busy="submitting ? 'true' : 'false'"><label v-for="entry in reviewEntries" :key="entry.key" class="review-row"><span><strong>{{ entry.label }}</strong><small>{{ 'warehouseID' in entry.item ? entry.item.warehouseID : `${entry.item.catalog}.${entry.item.schema}.${entry.item.table}` }}</small></span><span class="field"><span class="field-label">Faros resource name</span><input v-model="entry.name" autocomplete="off" :disabled="submitting || registrationFrozen" /><small v-if="resourceNameError(entry.name, 'Resource name')" class="error">{{ resourceNameError(entry.name, 'Resource name') }}</small></span></label><p v-if="reviewValidationError" class="error" role="alert">{{ reviewValidationError }}</p><p class="muted">Nothing is created until you confirm. Existing resources are never overwritten.</p></div>
        <div v-else-if="step === 'results'" class="result-list" aria-live="polite" :aria-busy="submitting ? 'true' : 'false'"><div class="result-summary"><Database :stroke-width="1.75" /><span><strong>Registration finished</strong><small>{{ summarizeRegistration(results) || 'No results returned.' }}</small></span></div><button v-if="retryableResults.length" class="secondary" type="button" :disabled="submitting" @click="retryFailed">{{ submitting ? 'Retrying…' : `Retry failed (${retryableResults.length})` }}</button><div v-for="result in results" :key="`${result.index}-${result.name}`" class="result-row"><code>{{ result.name || `Item ${result.index + 1}` }}</code><span :class="['result-state', result.state]">{{ result.state }}</span><small>{{ result.message || (result.state === 'created' ? 'Registration succeeded; validation pending.' : '') }}</small></div></div>
        <p v-if="branchChecking" class="import-loading" role="status" aria-live="polite"><LoaderCircle class="spin" :stroke-width="1.75" /> Loading the complete branch before selecting it…</p>
        <p v-if="submitting" class="import-loading" role="status" aria-live="polite"><LoaderCircle class="spin" :stroke-width="1.75" /> Registering selected {{ plural }}…</p>
        <p v-if="error" class="error" role="alert" aria-live="assertive">{{ error }}</p>
      </div>
      <footer class="import-actions"><button v-if="step === 'browse' || step === 'review'" class="secondary icon-text" type="button" :disabled="dialogBusy" @click="back"><ArrowLeft :stroke-width="1.75" /> Back</button><span class="import-spacer" /><button v-if="step === 'source'" class="primary" type="button" :disabled="initializationPending || submitting" @click="fromSource">Browse</button><button v-else-if="step === 'browse'" class="primary" type="button" :disabled="!tree.selectedLeafIds.length || dialogBusy" @click="review">Review {{ tree.selectedLeafIds.length }}</button><button v-else-if="step === 'review'" class="primary" type="button" :disabled="submitting || !!reviewValidationError" @click="register">{{ submitting ? 'Registering…' : `Register ${reviewEntries.length}` }}</button><button v-else class="primary" type="button" @click="close">Done</button></footer>
    </section>
  </div>
</template>
