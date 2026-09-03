<script setup lang="ts">
import { computed, inject, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ArrowLeft, ArrowRight, Database, LoaderCircle, X } from 'lucide-vue-next'
import { api } from './api'
import { contextGenerationKey } from './context'
import { formatDatabricksError } from './errors'
import type { DatabricksPrerequisiteKind } from './journey'
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
import { importPrerequisitesReady } from './tableRefs'
import {
  materializeRegistrationResults,
  mergeRegistrationResults,
  retryableRegistrationIndices,
  snapshotDiscoveryCoordinates,
  suggestedRegistrationName,
  summarizeRegistration,
} from './registrationFlow'
import { resourceNameError } from './resourceName'
import FormSelect from './portalkit/FormSelect.vue'
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
import type { Connection, Table, Warehouse } from './types'

type Kind = 'warehouse' | 'table'
type Step = 'source' | 'browse' | 'review' | 'results'

interface ReviewEntry { key: string; label: string; item: RegistrationItem; name: string }

interface ContextRailContent { title: string; description: string; points: readonly string[] }

interface ContinuationToken {
  generation: number
  context: number
}

const props = withDefaults(defineProps<{ kind: Kind; routeOwned?: boolean; backLabel?: string }>(), {
  routeOwned: false,
})
const emit = defineEmits<{
  (event: 'cancel'): void
  (event: 'complete', successful: boolean): void
  /** @deprecated Use cancel; retained for modal consumers on older portals. */
  (event: 'close'): void
  (event: 'registered'): void
  (event: 'prerequisite', kind: DatabricksPrerequisiteKind): void
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
const contextGeneration = inject(contextGenerationKey, ref(0))
let initializationRunGeneration = 0
let treeGeneration = 0
let submissionGeneration = 0
let focusGeneration = 0
let mounted = false
// The host advances the shared context synchronously, then Vue flushes the
// keyed replacement. Keep this instance inert during that scheduling window.
let mountedContextGeneration: number | null = null
let previousFocus: HTMLElement | null = null
const routeHeading = ref<HTMLElement | null>(null)
const routeAnnouncement = ref('')

const plural = computed(() => props.kind === 'warehouse' ? 'warehouses' : 'tables')
const matchingWarehouses = computed(() => warehouses.value.filter(item => item.connectionRef === connectionRef.value))
const connectionOptions = computed(() => connections.value.map(connection => ({ value: connection.name, label: connection.name })))
const warehouseOptions = computed(() => matchingWarehouses.value.map(warehouse => ({ value: warehouse.name, label: warehouse.name })))
const selectedConnectionReady = computed(() => !!connectionRef.value && connections.value.some(item => item.name === connectionRef.value))
const selectedNodes = computed(() => tree.value.selectedLeafIds.map(id => tree.value.nodes[id]).filter((node): node is RegistrationTreeNode => !!node && isLeaf(node)))
const requiredInitialization = computed<readonly InitializationResource[]>(() => props.kind === 'table' ? ['connections', 'warehouses', 'tables'] : ['connections', 'warehouses'])
const initializationPending = computed(() => requiredInitialization.value.some(resource => initializationState[resource] === 'loading'))
const initializationReady = computed(() => requiredInitialization.value.every(resource => initializationState[resource] === 'success'))
const sameConnectionWarehouseMissing = computed(() => props.kind === 'table'
  && initializationState.warehouses === 'success'
  && selectedConnectionReady.value
  && matchingWarehouses.value.length === 0)
const browseReady = computed(() => selectedConnectionReady.value
  && importPrerequisitesReady(props.kind, initializationState, connectionRef.value, warehouseRef.value, warehouses.value))
const browseGuidanceID = computed(() => `databricks-${props.kind}-browse-guidance`)
const browseGuidance = computed(() => {
  const blocker = initializationBlocker()
  if (blocker) return blocker
  if (!selectedConnectionReady.value) return 'Create or select a Databricks connection before browsing.'
  if (props.kind === 'table' && !matchingWarehouses.value.some(item => item.name === warehouseRef.value)) {
    return 'Register and select a SQL warehouse on this connection before browsing.'
  }
  return ''
})
const dialogBusy = computed(() => rootLoading.value || branchChecking.value || submitting.value || initializationPending.value || Object.values(tree.value.nodes).some(node => node.loading))
const failedResultIndices = computed(() => retryableRegistrationIndices(results.value))
const retryableResults = computed(() => failedResultIndices.value.filter(index => !!registrationItems.value[index]))
const registrationSucceeded = computed(() => results.value.some(result => result.state === 'created' || result.state === 'existing'))
const stepLabel = computed(() => ({ source: 'Choose source', browse: `Browse ${plural.value}`, review: 'Review', results: 'Results' })[step.value])
const backLabel = computed(() => props.backLabel || (props.kind === 'table' ? 'Tables' : 'Warehouses'))
const duplicateReviewNames = computed(() => {
  const counts = new Map<string, number>()
  for (const entry of reviewEntries.value) counts.set(entry.name, (counts.get(entry.name) || 0) + 1)
  return new Set([...counts.entries()].filter(([, count]) => count > 1).map(([name]) => name))
})
const contextRail = computed<ContextRailContent>(() => {
  if (step.value === 'source') {
    return {
      title: 'Choose a source',
      description: 'Start with the Databricks connection and, for tables, the SQL warehouse that should own the imported metadata.',
      points: [
        'Discovery reads metadata through the selected connection.',
        props.kind === 'table' ? 'Tables remain bound to a warehouse on the same connection.' : 'Warehouse registration keeps the Databricks warehouse reference intact.',
      ],
    }
  }
  if (step.value === 'browse') {
    return {
      title: 'Select what to register',
      description: `Choose up to ${REGISTRATION_LIMIT} supported ${plural.value} from the Databricks hierarchy. Expand a branch to load its next level.`,
      points: [
        'Existing Faros resources are shown but cannot be selected again.',
        'Only metadata is registered; Databricks data is never copied.',
      ],
    }
  }
  if (step.value === 'review') {
    return {
      title: 'Confirm resource names',
      description: 'Give each selected item a valid, unique Faros resource name before registration begins.',
      points: [
        'Nothing is created until you choose Register.',
        'Existing resources are never overwritten.',
      ],
    }
  }
  return {
    title: 'Registration status',
    description: 'Review each result before leaving this flow. A failed item can be retried without reselecting the hierarchy.',
    points: [
      'Created and existing results are safe to continue from.',
      'Registration success does not claim that the resource is ready or queryable yet.',
    ],
  }
})

function phaseLabel(resource: InitializationResource): string {
  return resource === 'connections' ? 'connections' : resource === 'warehouses' ? 'registered warehouses' : 'registered tables'
}

function isMountedContextCurrent(): boolean {
  return mounted && mountedContextGeneration !== null && contextGeneration.value === mountedContextGeneration
}

function isCurrentInitialization(resource: InitializationResource, token: ContinuationToken): boolean {
  return isMountedContextCurrent() && initializationGeneration[resource] === token.generation && contextGeneration.value === token.context
}

function isCurrentInitializationRun(token: ContinuationToken): boolean {
  return isMountedContextCurrent() && initializationRunGeneration === token.generation && contextGeneration.value === token.context
}

function isCurrentTree(token: ContinuationToken): boolean {
  return isMountedContextCurrent() && treeGeneration === token.generation && contextGeneration.value === token.context
}

function isCurrentSubmission(token: ContinuationToken): boolean {
  return isMountedContextCurrent() && submissionGeneration === token.generation && contextGeneration.value === token.context
}

function isCurrentFocus(token: ContinuationToken): boolean {
  return focusGeneration === token.generation && mountedContextGeneration === token.context && contextGeneration.value === token.context
}

function queryRoot<T extends HTMLElement>(selector: string): T | null {
  const element = root.value
  return element && typeof element.querySelector === 'function' ? element.querySelector<T>(selector) : null
}

function contextChangedError(): Error {
  return new Error('Connection changed while selecting the branch; nothing was selected.')
}

async function loadInitializationPhase(resource: InitializationResource, expectedContext = contextGeneration.value): Promise<void> {
  if (!isMountedContextCurrent() || expectedContext !== mountedContextGeneration) return
  const generation = ++initializationGeneration[resource]
  const token = { generation, context: expectedContext }
  initializationState[resource] = 'loading'; initializationErrors[resource] = null
  try {
    if (resource === 'connections') {
      const list = await api.listConnections(); if (!isCurrentInitialization(resource, token)) return
      connections.value = list
      if (!list.some(item => item.name === connectionRef.value)) connectionRef.value = list[0]?.name ?? ''
    } else if (resource === 'warehouses') {
      const list = await api.listWarehouses(); if (!isCurrentInitialization(resource, token)) return
      warehouses.value = list
      if (!list.some(item => item.name === warehouseRef.value && item.connectionRef === connectionRef.value)) warehouseRef.value = list.find(item => item.connectionRef === connectionRef.value)?.name ?? ''
    } else {
      const list = await api.listTables(); if (!isCurrentInitialization(resource, token)) return
      currentTables.value = list
    }
    if (!isCurrentInitialization(resource, token)) return
    initializationState[resource] = 'success'
  } catch (cause) {
    if (!isCurrentInitialization(resource, token)) return
    initializationState[resource] = 'error'; initializationErrors[resource] = formatDatabricksError(cause)
  }
}

async function initialize(): Promise<ContinuationToken | null> {
  const token = { generation: ++initializationRunGeneration, context: contextGeneration.value }
  await Promise.all(requiredInitialization.value.map(resource => loadInitializationPhase(resource, token.context)))
  return isCurrentInitializationRun(token) ? token : null
}
function retryInitialization(resource: InitializationResource): void {
  if (!isMountedContextCurrent() || initializationState[resource] === 'loading') return
  initializationRunGeneration += 1
  void loadInitializationPhase(resource)
}

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

async function fetchChildren(parent: RegistrationTreeNode, pageToken: string | undefined, token: ContinuationToken): Promise<TreePage> {
  if (!isCurrentTree(token)) throw contextChangedError()
  if (parent.kind === 'catalog') {
    const page = await api.discoverSchemas(connectionRef.value, parent.catalog || parent.label, pageToken)
    if (!isCurrentTree(token)) throw contextChangedError()
    return { nodes: page.items.map(item => schemaNode(item, parent.id)), nextPageToken: page.nextPageToken }
  }
  if (parent.kind === 'schema') {
    const page = await api.discoverTables(connectionRef.value, parent.catalog || '', parent.schema || parent.label, pageToken)
    if (!isCurrentTree(token)) throw contextChangedError()
    return { nodes: page.items.map(item => tableNode(item, parent.id)), nextPageToken: page.nextPageToken }
  }
  return { nodes: [] }
}

async function loadRoot(append = false): Promise<ContinuationToken | null> {
  if (!isMountedContextCurrent() || rootLoading.value) return null
  const treeToken = { generation: treeGeneration, context: contextGeneration.value }
  const pageToken = append ? rootNextPageToken.value : undefined
  rootLoading.value = true; rootError.value = ''
  try {
    const page = props.kind === 'warehouse' ? await api.discoverWarehouses(connectionRef.value, pageToken) : await api.discoverCatalogs(connectionRef.value, pageToken)
    if (!isCurrentTree(treeToken)) return null
    const nodes = props.kind === 'warehouse' ? (page.items as RemoteWarehouse[]).map(warehouseNode) : (page.items as RemoteCatalog[]).map(catalogNode)
    if (!append) tree.value = emptyRegistrationTree()
    appendTreePage(tree.value, undefined, { nodes })
    rootNextPageToken.value = page.nextPageToken
    return treeToken
  } catch (cause) {
    if (isCurrentTree(treeToken)) {
      rootError.value = formatDatabricksError(cause)
      return treeToken
    }
  }
  finally { if (isCurrentTree(treeToken)) rootLoading.value = false }
  return null
}

async function loadNodePage(id: string, append = false): Promise<void> {
  const node = tree.value.nodes[id]
  if (!isMountedContextCurrent() || !node || node.loading || isLeaf(node)) return
  const treeToken = { generation: treeGeneration, context: contextGeneration.value }
  node.loading = true; node.error = undefined
  try {
    const page = await fetchChildren(node, append ? node.nextPageToken : undefined, treeToken)
    if (!isCurrentTree(treeToken) || tree.value.nodes[id] !== node) return
    appendTreePage(tree.value, id, page)
  } catch (cause) { if (isCurrentTree(treeToken) && tree.value.nodes[id] === node) node.error = formatDatabricksError(cause) }
  finally { if (isCurrentTree(treeToken) && tree.value.nodes[id] === node) node.loading = false }
}

async function expandNode(id: string): Promise<void> {
  const node = tree.value.nodes[id]
  if (!isMountedContextCurrent() || !node || node.disabled || isLeaf(node)) return
  if (node.error) { node.expanded = true; await loadNodePage(id); return }
  node.expanded = !node.expanded
  if (node.expanded && !node.childrenLoaded) await loadNodePage(id)
}

async function toggleNode(id: string, checked: boolean): Promise<void> {
  const node = tree.value.nodes[id]
  if (!isMountedContextCurrent() || !node || node.disabled || submitting.value || branchChecking.value) return
  error.value = null
  if (isLeaf(node)) { error.value = updateLeafSelection(tree.value, id, checked); return }
  if (!checked) {
    const descendants = new Set(loadedSelectableLeaves(tree.value, id))
    tree.value.selectedLeafIds = tree.value.selectedLeafIds.filter(selected => !descendants.has(selected))
    return
  }
  const treeToken = { generation: treeGeneration, context: contextGeneration.value }
  const remaining = REGISTRATION_LIMIT - tree.value.selectedLeafIds.length
  branchChecking.value = true
  const result = await exhaustBranchSelection(tree.value, id, async (parent, pageToken) => {
    try {
      const page = await fetchChildren(parent, pageToken, treeToken)
      if (!isCurrentTree(treeToken)) throw contextChangedError()
      return page
    } catch (cause) {
      throw new Error(formatDatabricksError(cause))
    }
  }, remaining)
  if (isCurrentTree(treeToken)) {
    if (result.complete && result.state) { tree.value = result.state; tree.value.nodes[id].expanded = true }
    else error.value = result.reason || 'The complete branch could not be selected.'
    branchChecking.value = false
  }
}

function loadMore(parentId?: string): void { if (parentId) void loadNodePage(parentId, true); else void loadRoot(true) }

async function fromSource(): Promise<void> {
  if (!isMountedContextCurrent()) return
  const expectedContext = contextGeneration.value
  error.value = null
  const blocker = initializationBlocker(); if (blocker) { error.value = blocker; return }
  if (!selectedConnectionReady.value) { error.value = 'Select a connection.'; return }
  if (props.kind === 'table' && !matchingWarehouses.value.some(item => item.name === warehouseRef.value)) { error.value = 'Select a registered warehouse on this connection.'; return }
  resetTree(); step.value = 'browse'
  const treeToken = await loadRoot()
  if (!treeToken || !isCurrentTree(treeToken) || contextGeneration.value !== expectedContext) return
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
function reviewEntryError(entry: ReviewEntry): string | null {
  const invalid = resourceNameError(entry.name, 'Resource name')
  if (invalid) return invalid
  return duplicateReviewNames.value.has(entry.name) ? 'Choose a unique Faros resource name.' : null
}
function reviewInputID(index: number): string { return `import-review-name-${index}` }
function reviewErrorID(index: number): string { return `${reviewInputID(index)}-error` }
const reviewValidationError = computed(() => validateReviewEntries(reviewEntries.value))

function review(): void {
  if (!isMountedContextCurrent()) return
  const entries = selectedNodes.value.map(node => { const name = suggested(node); return { key: node.id, label: node.kind === 'warehouse' ? node.label : `${node.catalog}.${node.schema}.${node.table}`, item: registrationItem(node, name), name } })
  const invalid = validateReviewEntries(entries); if (invalid) { error.value = invalid; return }
  reviewEntries.value = entries; reviewCoordinates.value = coordinates(); registrationFrozen.value = false; error.value = null; step.value = 'review'
  focusStep()
}

function emitRegisteredIfNeeded(batch: readonly RegistrationResult[]): void { if (batch.some(result => result.state === 'created' || result.state === 'existing')) emit('registered') }

async function register(): Promise<void> {
  if (!isMountedContextCurrent() || submitting.value) return
  const submissionToken = { generation: ++submissionGeneration, context: contextGeneration.value }
  if (!isCurrentSubmission(submissionToken)) return
  const invalid = reviewValidationError.value; if (invalid) { error.value = invalid; return }
  const coordinatesSnapshot = reviewCoordinates.value ?? coordinates()
  const registrationItemsSnapshot = reviewEntries.value.map(entry => ({ ...entry.item, name: entry.name }))
  registrationItems.value = registrationItemsSnapshot
  registrationFrozen.value = true; submitting.value = true; error.value = null
  try {
    const response = await api.registerResources({ kind: props.kind, connectionRef: coordinatesSnapshot.connectionRef, warehouseRef: props.kind === 'table' ? coordinatesSnapshot.warehouseRef : undefined, items: registrationItemsSnapshot })
    if (!isCurrentSubmission(submissionToken)) return
    results.value = materializeRegistrationResults(registrationItemsSnapshot, response.results)
    step.value = 'results'
    if (!isCurrentSubmission(submissionToken)) return
    emitRegisteredIfNeeded(response.results)
    if (!isCurrentSubmission(submissionToken)) return
    focusStep()
    if (response.results.length !== registrationItemsSnapshot.length && isCurrentSubmission(submissionToken)) error.value = `Registration returned ${response.results.length} of ${registrationItemsSnapshot.length} expected results.`
  } catch (cause) {
    if (!isCurrentSubmission(submissionToken)) return
    results.value = materializeRegistrationResults(registrationItemsSnapshot, [])
    step.value = 'results'
    if (!isCurrentSubmission(submissionToken)) return
    error.value = formatDatabricksError(cause)
    focusStep()
  }
  finally { if (isCurrentSubmission(submissionToken)) submitting.value = false }
}

async function retryFailed(): Promise<void> {
  if (!isMountedContextCurrent() || submitting.value || !reviewCoordinates.value) return
  const submissionToken = { generation: ++submissionGeneration, context: contextGeneration.value }
  if (!isCurrentSubmission(submissionToken)) return
  const coordinatesSnapshot = reviewCoordinates.value
  const entries = retryableResults.value.map(index => ({ index, item: registrationItems.value[index] })).filter((entry): entry is { index: number; item: RegistrationItem } => !!entry.item)
  if (!entries.length) return
  const retryItems = entries.map(entry => entry.item)
  submitting.value = true; error.value = null
  try {
    const response = await api.registerResources({ kind: props.kind, connectionRef: coordinatesSnapshot.connectionRef, warehouseRef: props.kind === 'table' ? coordinatesSnapshot.warehouseRef : undefined, items: retryItems })
    if (!isCurrentSubmission(submissionToken)) return
    results.value = mergeRegistrationResults(results.value, retryItems, response.results, entries.map(entry => entry.index)); emitRegisteredIfNeeded(response.results)
  } catch (cause) { if (isCurrentSubmission(submissionToken)) error.value = formatDatabricksError(cause) }
  finally { if (isCurrentSubmission(submissionToken)) submitting.value = false }
}

function back(): void {
  if (!isMountedContextCurrent() || dialogBusy.value) return
  error.value = null
  if (step.value === 'browse') { resetTree(); step.value = 'source' }
  else if (step.value === 'review') { registrationFrozen.value = false; step.value = 'browse' }
  focusStep()
}
function resolvePrerequisite(kind: DatabricksPrerequisiteKind): void {
  if (!isMountedContextCurrent()) return
  previousFocus = null
  emit('prerequisite', kind)
}
function focusStep(): void {
  if (!mounted) return
  if (!isMountedContextCurrent()) return
  const token = { generation: ++focusGeneration, context: contextGeneration.value }
  void nextTick(() => {
    if (mounted && isCurrentFocus(token)) {
      const selector = step.value === 'source'
        ? '.import-body [data-form-select-trigger]:not(:disabled),.import-body button:not(:disabled),.import-body input:not(:disabled)'
        : step.value === 'browse'
          ? '[role="treeitem"][tabindex="0"],.import-body button:not(:disabled)'
          : step.value === 'review'
            ? '.import-body input:not(:disabled),.import-body button:not(:disabled)'
            : '.import-body button:not(:disabled)'
      // Route-owned registration is a real page transition. Put focus on its
      // heading so the destination is announced consistently, even when the
      // first control is disabled while prerequisites settle.
      const target = props.routeOwned ? routeHeading.value ?? queryRoot<HTMLElement>(selector) : queryRoot<HTMLElement>(selector)
      target?.focus()
      if (props.routeOwned) announceRoute(routeStepAnnouncement())
    }
  })
}

function routeStepAnnouncement(): string {
  if (step.value === 'source') {
    if (initializationPending.value) return `Register ${plural.value}. Loading prerequisites.`
    if (!browseReady.value) return `Register ${plural.value}. ${browseGuidance.value}`
    return `Register ${plural.value}. Choose a source, then browse Databricks metadata.`
  }
  if (step.value === 'browse') return `Browse ${plural.value}. Select resources to continue to review.`
  if (step.value === 'review') return `Review selected ${plural.value} before registering them.`
  return `Registration results for ${plural.value} are ready.`
}

function announceRoute(message: string): void {
  if (!props.routeOwned) return
  // Clear first so a repeated visit to the same route still produces a live
  // region announcement for assistive technology.
  routeAnnouncement.value = ''
  void nextTick(() => {
    if (mounted && isMountedContextCurrent()) routeAnnouncement.value = message
  })
}
function focusDialog(): void {
  if (props.routeOwned || !mounted) return
  if (!isMountedContextCurrent()) return
  const token = { generation: ++focusGeneration, context: contextGeneration.value }
  void nextTick(() => {
    if (mounted && isCurrentFocus(token)) {
      queryRoot<HTMLElement>('.import-head button:not(:disabled),.import-body [data-form-select-trigger]:not(:disabled),.import-body button:not(:disabled),.import-body input:not(:disabled),[role="treeitem"]')?.focus()
    }
  })
}
function restoreFocus(): void {
  if (props.routeOwned) { previousFocus = null; return }
  const target = previousFocus
  previousFocus = null
  if (!target) return
  const token = { generation: ++focusGeneration, context: contextGeneration.value }
  void nextTick(() => {
    if (isCurrentFocus(token) && target.isConnected) target.focus()
  })
}
function cancel(): void {
  if (!isMountedContextCurrent() || submitting.value) return
  restoreFocus()
  emit('cancel')
  // Keep the former close event for hosts that still mount this wizard as a
  // modal. Route-owned hosts listen to the explicit cancel event above.
  emit('close')
}
function complete(): void {
  if (!isMountedContextCurrent() || submitting.value || step.value !== 'results') return
  restoreFocus()
  // A results page containing only failures is not prerequisite success. The
  // host can therefore return to the origin instead of advancing the journey.
  // Modal consumers predate the route-owned completion event and close on the
  // legacy `close` event. Keep that contract mode-specific so a modal Done
  // action still dismisses it while route-owned Done reports its result to the
  // host for navigation.
  if (!props.routeOwned) {
    emit('close')
    return
  }
  emit('complete', registrationSucceeded.value)
}
function backdropPointerDown(event: PointerEvent): void {
  if (!props.routeOwned && event.target === event.currentTarget) cancel()
}
function dialogTabStops(): HTMLElement[] {
  const element = root.value
  if (!element || typeof element.querySelectorAll !== 'function') return []
  return [...element.querySelectorAll<HTMLElement>('button:not(:disabled),input:not(:disabled),[data-form-select-trigger]:not(:disabled),[role="treeitem"][tabindex="0"]')]
    .filter(element => element.tabIndex === 0)
}
function keydown(event: KeyboardEvent): void {
  if (props.routeOwned) return
  if (!isMountedContextCurrent()) return
  if (event.key === 'Escape') { event.preventDefault(); cancel(); return }
  if (event.key !== 'Tab' || !root.value) return
  const focusable = dialogTabStops()
  const first = focusable[0], last = focusable.at(-1); if (!first || !last) return
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
}

watch(connectionRef, (next, previous) => { if (next !== previous) { resetTree(); if (step.value !== 'source') step.value = 'source'; if (!matchingWarehouses.value.some(item => item.name === warehouseRef.value)) warehouseRef.value = matchingWarehouses.value[0]?.name ?? '' } })
watch(warehouseRef, (next, previous) => { if (next !== previous) { resetTree(); if (step.value !== 'source') step.value = 'source' } })
onMounted(() => {
  mounted = true
  mountedContextGeneration = contextGeneration.value
  if (!props.routeOwned) {
    previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
    focusDialog()
  } else {
    focusStep()
  }
  void initialize().then(token => {
    if (token && isCurrentInitializationRun(token)) focusStep()
  })
})
onBeforeUnmount(() => {
  mounted = false
  initializationRunGeneration += 1
  treeGeneration += 1
  submissionGeneration += 1
  for (const resource of ['connections', 'warehouses', 'tables'] as const) initializationGeneration[resource] += 1
  restoreFocus()
})
</script>

<template>
  <div :class="props.routeOwned ? 'k-create-page import-route' : 'import-backdrop'" @pointerdown="backdropPointerDown">
    <button v-if="props.routeOwned" class="k-btn k-btn--ghost k-back-action" type="button" :disabled="submitting" @click="cancel"><ArrowLeft :stroke-width="1.75" /> {{ backLabel }}</button>
    <header v-if="props.routeOwned" class="k-create-header"><h2 id="registration-title" ref="routeHeading" class="k-create-title" tabindex="-1">Register {{ plural }}</h2><p id="registration-description" class="k-create-description">Browse Databricks metadata and register selected {{ plural }}.</p></header>
    <p v-if="props.routeOwned" class="sr-only" role="status" aria-live="polite" aria-atomic="true">{{ routeAnnouncement }}</p>
    <section ref="root" :class="['import-dialog', { 'import-dialog--route k-create-surface k-create-surface--wide': props.routeOwned }]" :role="props.routeOwned ? undefined : 'dialog'" :aria-modal="props.routeOwned ? undefined : 'true'" aria-labelledby="registration-title" aria-describedby="registration-description" :aria-busy="dialogBusy ? 'true' : 'false'" @keydown="keydown">
      <header v-if="!props.routeOwned" class="import-head"><div><span class="import-eyebrow">{{ stepLabel }}</span><h2 id="registration-title">New {{ kind }}</h2><p id="registration-description">Browse Databricks metadata and register selected {{ plural }}.</p></div><button class="k-btn k-btn--ghost databricks-dialog-close" type="button" aria-label="Close" :disabled="submitting" @click="cancel"><X :stroke-width="1.75" /></button></header>
      <div class="import-workbench">
        <div class="import-primary">
          <ol class="import-steps" aria-label="Import progress"><li :aria-current="step === 'source' ? 'step' : undefined">Source</li><li :aria-current="step === 'browse' ? 'step' : undefined">Browse</li><li :aria-current="step === 'review' ? 'step' : undefined">Review</li><li :aria-current="step === 'results' ? 'step' : undefined">Results</li></ol>
          <div :class="['import-body', `import-body--${step}`]">
            <div v-if="step === 'source'" class="import-stack">
              <div class="field">
                <label id="import-connection-label" class="field-label" for="import-connection">Connection</label>
                <FormSelect
                  id="import-connection"
                  v-model="connectionRef"
                  name="connectionRef"
                  :options="connectionOptions"
                  placeholder="Select connection"
                  :disabled="initializationPending || submitting"
                  required
                  labelledby="import-connection-label"
                  describedby="import-connection-hint"
                />
                <span id="import-connection-hint" class="field-hint">Discovery uses this connection's Databricks credentials.</span>
              </div>
              <div v-if="kind === 'table'" class="field">
                <label id="import-warehouse-label" class="field-label" for="import-warehouse">Query warehouse</label>
                <FormSelect
                  id="import-warehouse"
                  v-model="warehouseRef"
                  name="warehouseRef"
                  :options="warehouseOptions"
                  placeholder="Select warehouse"
                  :disabled="initializationPending || submitting"
                  required
                  labelledby="import-warehouse-label"
                  describedby="import-warehouse-hint"
                />
                <span id="import-warehouse-hint" class="field-hint">Imported tables remain bound to this same-connection warehouse.</span>
              </div>
              <div class="initialization-status" :aria-busy="initializationPending ? 'true' : 'false'">
                <p v-if="initializationReady" :class="['initialization-summary', { 'initialization-summary--ready': browseReady }]" role="status" aria-live="polite">{{ browseReady ? 'Prerequisites ready.' : 'Prerequisite checks complete.' }}</p>
                <template v-else v-for="resource in requiredInitialization" :key="resource">
                  <p v-if="initializationState[resource] === 'loading'" class="initialization-phase" role="status" aria-live="polite">Loading {{ phaseLabel(resource) }}…</p>
                  <p v-else-if="initializationState[resource] === 'error'" class="initialization-phase error" role="alert" aria-live="assertive">
                    <span>{{ phaseLabel(resource) }} could not be loaded: {{ initializationErrors[resource] }}</span>
                    <button class="k-btn k-btn--ghost databricks-inline-action" type="button" @click="retryInitialization(resource)">Retry</button>
                  </p>
                </template>
              </div>
              <div v-if="!connections.length && !initializationPending && initializationState.connections === 'success'" class="prerequisite" role="status">
                <span class="prerequisite-copy">A connection is required.</span>
                <button class="k-btn k-btn--ghost prerequisite-action" type="button" @click="resolvePrerequisite('connection')">
                  Create connection <ArrowRight :size="14" :stroke-width="1.75" aria-hidden="true" />
                </button>
              </div>
              <div v-else-if="sameConnectionWarehouseMissing" class="prerequisite" role="status">
                <span class="prerequisite-copy">A registered warehouse on this connection is required.</span>
                <button class="k-btn k-btn--ghost prerequisite-action" type="button" @click="resolvePrerequisite('warehouse')">
                  Register warehouse <ArrowRight :size="14" :stroke-width="1.75" aria-hidden="true" />
                </button>
              </div>
              <p v-if="!browseReady" :id="browseGuidanceID" class="sr-only">{{ browseGuidance }}</p>
            </div>
            <LazyCheckboxTree v-else-if="step === 'browse'" :tree="tree" :label="`Available ${plural}`" :root-loading="rootLoading" :root-error="rootError" :root-next-page-token="rootNextPageToken" :busy="branchChecking || submitting" @expand="expandNode" @toggle="toggleNode" @load-more="loadMore" />
            <div v-else-if="step === 'review'" class="review-list" :aria-busy="submitting ? 'true' : 'false'">
              <div v-for="(entry, index) in reviewEntries" :key="entry.key" class="review-row">
                <span><strong>{{ entry.label }}</strong><small>{{ 'warehouseID' in entry.item ? entry.item.warehouseID : `${entry.item.catalog}.${entry.item.schema}.${entry.item.table}` }}</small></span>
                <span class="field">
                  <label class="field-label" :for="reviewInputID(index)">Faros resource name</label>
                  <input
                    :id="reviewInputID(index)"
                    v-model="entry.name"
                    class="k-input"
                    autocomplete="off"
                    :disabled="submitting || registrationFrozen"
                    :aria-invalid="reviewEntryError(entry) ? 'true' : undefined"
                    :aria-describedby="reviewEntryError(entry) ? reviewErrorID(index) : undefined"
                  >
                  <small v-if="reviewEntryError(entry)" :id="reviewErrorID(index)" class="error" role="alert">{{ reviewEntryError(entry) }}</small>
                </span>
              </div>
              <p v-if="reviewValidationError" class="error" role="alert">{{ reviewValidationError }}</p>
              <p class="muted">Nothing is created until you confirm. Existing resources are never overwritten.</p>
            </div>
            <div v-else-if="step === 'results'" class="result-list" aria-live="polite" :aria-busy="submitting ? 'true' : 'false'"><div class="result-summary"><Database :stroke-width="1.75" /><span><strong>Registration finished</strong><small>{{ summarizeRegistration(results) || 'No results returned.' }}</small></span></div><button v-if="retryableResults.length" class="k-btn k-btn--ghost" type="button" :disabled="submitting" @click="retryFailed">{{ submitting ? 'Retrying…' : `Retry failed (${retryableResults.length})` }}</button><div v-for="result in results" :key="`${result.index}-${result.name}`" class="result-row"><code>{{ result.name || `Item ${result.index + 1}` }}</code><span :class="['result-state', result.state]">{{ result.state }}</span><small>{{ result.message || (result.state === 'created' ? 'Registration succeeded; validation pending.' : '') }}</small></div></div>
            <p v-if="branchChecking" class="import-loading" role="status" aria-live="polite"><LoaderCircle class="spin" :stroke-width="1.75" /> Loading the complete branch before selecting it…</p>
            <p v-if="submitting" class="import-loading" role="status" aria-live="polite"><LoaderCircle class="spin" :stroke-width="1.75" /> Registering selected {{ plural }}…</p>
            <p v-if="error" class="error" role="alert" aria-live="assertive">{{ error }}</p>
          </div>
          <footer :class="props.routeOwned ? 'k-create-actions' : 'import-actions'"><button v-if="step === 'browse' || step === 'review'" class="k-btn k-btn--ghost icon-text" type="button" :disabled="dialogBusy" @click="back"><ArrowLeft :stroke-width="1.75" /> Back</button><span class="import-spacer" /><button v-if="props.routeOwned && step !== 'results'" class="k-btn k-btn--ghost" type="button" :disabled="submitting" @click="cancel">Cancel</button><button v-if="step === 'source'" class="k-btn k-btn--primary" type="button" :disabled="!browseReady || initializationPending || submitting" :aria-describedby="!browseReady ? browseGuidanceID : undefined" @click="fromSource">Browse</button><button v-else-if="step === 'browse'" class="k-btn k-btn--primary" type="button" :disabled="!tree.selectedLeafIds.length || dialogBusy" @click="review">Review {{ tree.selectedLeafIds.length }}</button><button v-else-if="step === 'review'" class="k-btn k-btn--primary" type="button" :disabled="submitting || !!reviewValidationError" @click="register">{{ submitting ? 'Registering…' : `Register ${reviewEntries.length}` }}</button><button v-else class="k-btn k-btn--primary" type="button" @click="complete">Done</button></footer>
        </div>
        <aside v-if="props.routeOwned" class="import-context-rail" aria-labelledby="import-context-title">
          <h3 id="import-context-title">{{ contextRail.title }}</h3>
          <p>{{ contextRail.description }}</p>
          <dl class="import-context-values">
            <dt>Step</dt><dd>{{ stepLabel }}</dd>
            <dt>Connection</dt><dd><code>{{ connectionRef || 'Not selected' }}</code></dd>
            <template v-if="kind === 'table'">
              <dt>Warehouse</dt><dd><code>{{ warehouseRef || 'Not selected' }}</code></dd>
            </template>
            <dt>Selected</dt><dd>{{ tree.selectedLeafIds.length }} of {{ REGISTRATION_LIMIT }}</dd>
          </dl>
          <ul>
            <li v-for="point in contextRail.points" :key="point">{{ point }}</li>
          </ul>
        </aside>
      </div>
    </section>
  </div>
</template>
