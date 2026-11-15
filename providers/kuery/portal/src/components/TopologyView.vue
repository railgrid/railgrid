<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ChevronDown } from 'lucide-vue-next'

import type { RailgridContext } from '../element'
import type { ObjectResult, QuerySpec, QueryStatus } from '../api'
import {
  buildTopologyElements, deriveTopologyTree, forceLayoutOptions, graphKeyAction, IMPACT_RELATIONS, mountGraph, relationElements, themeStyle,
  type GraphHandle, type GraphHooks,
} from '../graph'
import { createKueryRequestContext, isNoEngagedEdgesError, resourceLabel, safeHTTPStatus, useKueryApi } from '../kuery'
import FormSelect from '../portalkit/FormSelect.vue'

const props = defineProps<{ context: RailgridContext | null; edges: string[]; active: boolean; savedView: string }>()
const emit = defineEmits<{ inspect: [row: ObjectResult] }>()
const context = computed(() => props.context)
const { api, query } = useKueryApi(context, computed(() => props.savedView))
const rows = ref<ObjectResult[]>([])
const loaded = ref(false)
const loading = ref(false)
const error = ref('')
const errorDetail = ref('')
const notEngaged = ref(false)
const incomplete = ref(false)
const edge = ref('')
const snapshotEdge = ref<string | null>(null)
const currentSnapshot = computed(() => loaded.value && snapshotEdge.value === edge.value)
const kind = ref('')
const namespace = ref('')
const layout = ref<'breadthfirst' | 'concentric' | 'circle' | 'cose'>('breadthfirst')
const representation = ref<'graph' | 'list'>('graph')
const graphHost = ref<HTMLElement | null>(null)
const panel = ref<HTMLElement | null>(null)
const graphError = ref('')
const graphRetrying = ref(false)
const expansionFailure = ref<{ id: string | null; message: string } | null>(null)
const full = ref(false)
const expandingAll = ref(false)
const expansionStatus = ref('')
// Force (cose) layout activity: it runs across animation frames on larger
// graphs so the page stays interactive; the toolbar offers Stop while it runs.
const layoutRunning = ref(false)
const layoutNodeCount = ref(0)
const responseWarnings = ref<string[]>([])
const relationResponseTruncated = ref(false)
const relationLimitReached = ref(false)
const expansionBound = ref<'nodes' | 'rounds' | null>(null)
const memberLimitReached = computed(() => rows.value.some(cluster => (cluster.relations?.members?.length ?? 0) >= 1000))
let loadController: AbortController | null = null
let loadGeneration = 0
let graph: GraphHandle | null = null
let graphGeneration = 0
let graphObjects = new Map<string, ObjectResult>()
let graphController: AbortController | null = null
let expandAllController: AbortController | null = null
let expansionGeneration = 0
const expansionRequests = new Map<string, number>()
let fullscreenGeneration = 0
const requestContext = computed(() => createKueryRequestContext(context.value))

const tree = computed(() => deriveTopologyTree(rows.value, { kind: kind.value, namespace: namespace.value }))
const facets = computed(() => {
  const kinds = new Set<string>(); const namespaces = new Set<string>()
  for (const cluster of rows.value) for (const member of cluster.relations?.members ?? []) {
    if (member.object?.kind) kinds.add(member.object.kind)
    if (member.object?.metadata?.namespace) namespaces.add(member.object.metadata.namespace)
  }
  return { kinds: [...kinds].sort(), namespaces: [...namespaces].sort() }
})
const edgeOptions = computed(() => [{ value: '', label: 'All edges' }, ...props.edges.map(value => ({ value, label: value }))])
const kindOptions = computed(() => [{ value: '', label: 'All kinds' }, ...facets.value.kinds.map(value => ({ value, label: value }))])
const namespaceOptions = computed(() => [{ value: '', label: 'All namespaces' }, ...facets.value.namespaces.map(value => ({ value, label: value }))])
const layoutOptions = [
  { value: 'breadthfirst', label: 'Tree' }, { value: 'concentric', label: 'Radial' },
  { value: 'circle', label: 'Circle' }, { value: 'cose', label: 'Force' },
]

function topologySpec(): QuerySpec {
  return {
    root: 'clusters', cluster: edge.value ? { name: edge.value } : undefined,
    objects: { id: true, cluster: true, relations: { members: { limit: 1000, objects: { id: true, cluster: true, object: { kind: true, apiVersion: true, metadata: { name: true, namespace: true } } } } } },
  }
}

async function load(): Promise<void> {
  if (!api.value) return
  loadController?.abort()
  const controller = new AbortController()
  const requestGeneration = ++loadGeneration
  const request = requestContext.value
  const requestIdentity = request.identity
  const hostFetch = context.value?.fetch
  loadController = controller
  loading.value = true
  const isCurrent = (): boolean => {
    if (loadController !== controller || loadGeneration !== requestGeneration) return false
    return requestContext.value.identity === requestIdentity && context.value?.fetch === hostFetch
  }
  try {
    const result = await query(topologySpec(), controller.signal)
    if (!isCurrent()) return
    rows.value = result.objects ?? []; incomplete.value = !!result.incomplete; loaded.value = true; snapshotEdge.value = edge.value
    error.value = ''; errorDetail.value = ''; notEngaged.value = false
    responseWarnings.value = result.warnings ?? []
  } catch (reason) {
    if (!isCurrent()) return
    if (isNoEngagedEdgesError(reason)) {
      error.value = ''; errorDetail.value = ''
      notEngaged.value = true
      return
    }
    if (reason instanceof DOMException && reason.name === 'AbortError') return
    notEngaged.value = false
    error.value = 'Could not load fleet topology.'
    const status = safeHTTPStatus(reason)
    errorDetail.value = status === null ? '' : `Topology request returned HTTP ${status}.`
  } finally {
    if (isCurrent()) loading.value = false
  }
}

async function retryLoad(): Promise<void> {
  if (loading.value) return
  const focused = typeof document !== 'undefined' ? document.activeElement : null
  await load()
  await nextTick()
  if (focused && !focused.isConnected && document.activeElement === document.body) panel.value?.querySelector<HTMLElement>('#topology-title')?.focus()
}

// layoutConfig builds the Cytoscape layout for the selected option. The
// force layout is sized to the graph (see forceLayoutOptions): nodeCount is
// the node total the layout will run over — the live graph's by default, or
// the element count on first mount when there is no graph yet. incremental
// (default) starts from the current positions; a fresh mount randomizes.
function layoutConfig(options: { incremental?: boolean; nodeCount?: number } = {}): Record<string, unknown> {
  if (layout.value === 'concentric') return { name: 'concentric', concentric: (node: { data: (key: string) => unknown }) => node.data('tier') === 'cluster' ? 3 : node.data('tier') === 'namespace' ? 2 : 1, levelWidth: () => 1, minNodeSpacing: 30, padding: 20 }
  if (layout.value === 'circle') return { name: 'circle', padding: 20 }
  if (layout.value === 'cose') return forceLayoutOptions(options.nodeCount ?? graph?.nodeCount() ?? 0, { incremental: options.incremental ?? true })
  return { name: 'breadthfirst', directed: true, spacingFactor: 1, padding: 20 }
}
function stopLayout(): void { graph?.stopLayout() }

function destroyGraph(): void {
  graphGeneration += 1
  graphController?.abort()
  graphController = null
  expandAllController?.abort()
  expandAllController = null
  graph?.destroy()
  graph = null
  graphObjects.clear()
  expansionRequests.clear()
  relationResponseTruncated.value = false
  relationLimitReached.value = false
  expansionBound.value = null
  expandingAll.value = false
  expansionStatus.value = ''
  layoutRunning.value = false
  layoutNodeCount.value = 0
}

async function mount(preserveError = false): Promise<void> {
  destroyGraph(); if (!preserveError) graphError.value = ''; expansionFailure.value = null
  if (!currentSnapshot.value || !graphHost.value || representation.value !== 'graph' || tree.value.edges.length === 0) return
  const generation = graphGeneration
  graphController = new AbortController()
  const built = buildTopologyElements(rows.value, { kind: kind.value, namespace: namespace.value })
  graphObjects = new Map(Object.entries(built.nodeIndex))
  const nodeCount = built.elements.filter(element => !element.data?.source).length
  const hooks: GraphHooks = {
    onLayout: (running, nodes) => {
      if (generation !== graphGeneration) return
      layoutRunning.value = running
      layoutNodeCount.value = nodes
    },
  }
  try {
    const handle = await mountGraph(graphHost.value, built.elements, themeStyle(graphHost.value), id => { if (generation === graphGeneration) void expand(id) }, layoutConfig({ incremental: false, nodeCount }), hooks)
    if (generation !== graphGeneration) return handle.destroy()
    graph = handle
    graphError.value = ''
    requestAnimationFrame(() => { if (generation === graphGeneration && graph === handle) handle.fit() })
  } catch (reason) {
    if (generation === graphGeneration) graphError.value = 'Could not display the graph. Retry the graph or use the List representation.'
  }
}

async function retryGraph(): Promise<void> {
  if (graphRetrying.value) return
  const focused = typeof document !== 'undefined' ? document.activeElement : null
  graphRetrying.value = true
  try {
    await mount(true)
    await nextTick()
    if (focused && !focused.isConnected && document.activeElement === document.body) panel.value?.querySelector<HTMLElement>('#topology-title')?.focus()
  } finally {
    graphRetrying.value = false
  }
}

const impactRelations = (): Record<string, { limit?: number }> => Object.fromEntries(IMPACT_RELATIONS.map(name => [name, name === 'namespaced' ? { limit: 200 } : {}]))
interface RelationQueryResult {
  object: ObjectResult | null
  incomplete: boolean
  warnings: string[]
}

async function relationQuery(row: ObjectResult, signal?: AbortSignal): Promise<RelationQueryResult> {
  const metadata = row.object?.metadata ?? {}; const apiVersion = row.object?.apiVersion || ''; const apiGroup = apiVersion.includes('/') ? apiVersion.split('/')[0] : ''
  const filter = { name: metadata.name, ...(row.object?.kind ? { groupKind: { apiGroup, kind: row.object.kind } } : {}), ...(metadata.namespace ? { namespace: metadata.namespace } : {}) }
  const status = await query({ maxDepth: 5, cluster: row.cluster ? { name: row.cluster } : undefined, filter: { objects: [filter] }, objects: { id: true, cluster: true, relations: impactRelations() } }, signal)
  return { object: status.objects?.[0] ?? null, incomplete: !!status.incomplete, warnings: status.warnings ?? [] }
}

function recordRelationResult(result: RelationQueryResult): void {
  relationResponseTruncated.value ||= result.incomplete
  if (result.warnings.length) responseWarnings.value = [...new Set([...responseWarnings.value, ...result.warnings])]
  if (result.object && (result.object.relations?.namespaced?.length ?? 0) >= 200) relationLimitReached.value = true
}

function recordRelationStatus(status: QueryStatus): void {
  relationResponseTruncated.value ||= !!status.incomplete
  if (status.warnings?.length) responseWarnings.value = [...new Set([...responseWarnings.value, ...status.warnings])]
  if (status.objects?.some(object => (object.relations?.namespaced?.length ?? 0) >= 200)) relationLimitReached.value = true
}

async function expand(id: string): Promise<void> {
  const handle = graph; const controller = graphController; const currentGraphGeneration = graphGeneration; const row = graphObjects.get(id)
  if (!handle || !row || expandingAll.value) return
  if (handle.isExpanded(id)) {
    expansionRequests.set(id, ++expansionGeneration)
    handle.collapseFrom(id)
    return
  }
  const requestGeneration = ++expansionGeneration
  expansionRequests.set(id, requestGeneration)
  const isCurrent = () => graph === handle
    && graphGeneration === currentGraphGeneration
    && graphController === controller
    && expansionRequests.get(id) === requestGeneration
    && handle.hasNode(id)
    && handle.isExpanded(id)
  expansionFailure.value = null
  handle.markExpanded(id, true)
  try {
    const result = await relationQuery(row, controller?.signal)
    if (!isCurrent()) return
    recordRelationResult(result)
    if (!result.object) {
      handle.markExpanded(id, false)
      expansionFailure.value = { id, message: `No relationship result was returned for ${resourceLabel(row)}. Retry expansion; the current graph is unchanged.` }
      return
    }
    const built = relationElements(id, result.object); handle.add(built.elements)
    for (const [key, object] of Object.entries(built.nodeIndex)) graphObjects.set(key, object)
    void handle.relayout(layoutConfig())
  } catch (reason) {
    if (!isCurrent()) return
    handle.markExpanded(id, false)
    if (reason instanceof DOMException && reason.name === 'AbortError') return
    expansionFailure.value = { id, message: 'Could not expand this node. Retry expansion; the current graph is unchanged.' }
  }
}

async function expandAll(): Promise<void> {
  const handle = graph; const controller = graphController; const currentGraphGeneration = graphGeneration
  if (!handle || expandingAll.value) return
  const current = new AbortController()
  expandAllController = current
  const isCurrent = () => graph === handle && graphGeneration === currentGraphGeneration && graphController === controller && expandAllController === current && !current.signal.aborted
  expandingAll.value = true
  expansionStatus.value = 'Starting bounded graph expansion…'
  let rounds = 0
  let layoutDirty = false
  try {
    for (; rounds < 30 && isCurrent() && handle.nodeCount() < 4000; rounds += 1) {
      const ids = [...graphObjects.keys()].filter(id => handle.hasNode(id) && !handle.isExpanded(id)).slice(0, 300)
      if (!ids.length) break
      const status = await query({ maxDepth: 5, limit: ids.length, filter: { objects: ids.map(id => ({ id })) }, objects: { id: true, cluster: true, relations: impactRelations() } }, current.signal)
      let added = 0
      if (!isCurrent()) break
      recordRelationStatus(status)
      for (const object of status.objects ?? []) {
        if (handle.nodeCount() >= 4000) break
        if (!object.id) continue
        const built = relationElements(object.id, object)
        added += handle.add(built.elements, 4000).length
        for (const [key, row] of Object.entries(built.nodeIndex)) {
          if (handle.hasNode(key)) graphObjects.set(key, row)
        }
      }
      ids.forEach(id => handle.markExpanded(id, true))
      layoutDirty ||= added > 0
      expansionStatus.value = `Expansion round ${rounds + 1}: ${handle.nodeCount().toLocaleString()} graph nodes.`
      // Intermediate relayouts keep the discrete layouts readable as the
      // graph grows. The force layout is a full simulation over every node,
      // so it runs once, over the final graph, after expansion.
      if ((rounds + 1) % 3 === 0 && layoutDirty && layout.value !== 'cose') {
        void handle.relayout(layoutConfig())
        layoutDirty = false
      }
      if (added === 0) break
      await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
    }
    if (isCurrent()) {
      if (layoutDirty) void handle.relayout(layoutConfig())
      if (handle.nodeCount() >= 4000) expansionBound.value = 'nodes'
      else if (rounds >= 30) expansionBound.value = 'rounds'
      expansionStatus.value = `Expansion complete: ${handle.nodeCount().toLocaleString()} graph nodes.`
    }
  } catch (reason) {
    if (!isCurrent()) return
    if (reason instanceof DOMException && reason.name === 'AbortError') return
    expansionFailure.value = { id: null, message: 'Could not expand all nodes. Retry Expand all; the graph remains usable.' }
  } finally {
    if (expandAllController === current) {
      expandAllController = null
      expandingAll.value = false
    }
  }
}
function cancelExpandAll(): void {
  if (!expandAllController) return
  expandAllController.abort()
  expandAllController = null
  expandingAll.value = false
  expansionStatus.value = `Expansion cancelled. ${graph?.nodeCount().toLocaleString() ?? 0} graph nodes remain available.`
  void graph?.relayout(layoutConfig())
}
function retryExpansion(): void {
  const failure = expansionFailure.value
  expansionFailure.value = null
  if (failure?.id) void expand(failure.id)
  else void expandAll()
}
function resetGraph(): void { void mount() }
async function toggleFullscreen(): Promise<void> {
  const target = panel.value; if (!target) return
  const requestGeneration = ++fullscreenGeneration
  // A rejected request enters the CSS fallback. Once there, do not retry the
  // rejected native request: the next activation must deterministically exit
  // the fallback, even when the browser rejects every requestFullscreen call.
  if (!document.fullscreenElement && full.value) full.value = false
  else if (document.fullscreenElement) await document.exitFullscreen()
  else if (target.requestFullscreen) {
    try { await target.requestFullscreen() } catch { if (requestGeneration === fullscreenGeneration && !document.fullscreenElement) full.value = true }
  } else full.value = true
  requestAnimationFrame(() => graph?.fit())
}
function fullscreenChanged(): void { full.value = document.fullscreenElement === panel.value; requestAnimationFrame(() => graph?.fit()) }
function graphKeydown(event: KeyboardEvent): void {
  if (!graph) return
  const action = graphKeyAction(event.key); let handled = true
  if (action === 'pan-up') graph.panBy(0, 70); else if (action === 'pan-down') graph.panBy(0, -70)
  else if (action === 'pan-left') graph.panBy(70, 0); else if (action === 'pan-right') graph.panBy(-70, 0)
  else if (action === 'zoom-in') graph.zoomBy(1.15); else if (action === 'zoom-out') graph.zoomBy(1 / 1.15)
  else if (action === 'fullscreen') void toggleFullscreen(); else if (action === 'escape' && full.value) void toggleFullscreen(); else handled = false
  if (handled) event.preventDefault()
}

watch(edge, () => { error.value = ''; errorDetail.value = ''; notEngaged.value = false; void load() })
watch([rows, kind, namespace, representation, currentSnapshot], () => nextTick(() => void mount()), { deep: false })
watch(layout, () => graph ? void graph.relayout(layoutConfig()) : nextTick(() => void mount()))
watch(() => context.value?.theme, () => { if (graph && graphHost.value) graph.restyle(themeStyle(graphHost.value)) })
watch(() => props.active, active => { if (active) requestAnimationFrame(() => graph?.fit()) })
watch([api, requestContext, () => context.value?.fetch], ([ready, current, hostFetch], [wasReady, previous, previousFetch]) => {
  const authorityChanged = current.scopeIdentity !== previous.scopeIdentity || hostFetch !== previousFetch ||
    (!hostFetch && current.token !== previous.token)
  const edgeChanged = authorityChanged && edge.value !== ''
  if (authorityChanged) {
    const focused = typeof document !== 'undefined' ? document.activeElement : null
    const focusWasInside = !!focused && !!panel.value?.contains(focused)
    loadController?.abort(); loadGeneration += 1
    destroyGraph()
    rows.value = []; loaded.value = false; loading.value = false; snapshotEdge.value = null
    error.value = ''; errorDetail.value = ''; notEngaged.value = false; incomplete.value = false
    responseWarnings.value = []; graphError.value = ''; expansionFailure.value = null
    edge.value = ''; kind.value = ''; namespace.value = ''
    if (focusWasInside) void nextTick(() => {
      if (!focused?.isConnected && document.activeElement === document.body) panel.value?.querySelector<HTMLElement>('#topology-title')?.focus()
    })
  }
  // Resetting a selected edge schedules the edge watcher, which starts the read.
  if (ready && (!wasReady || current.identity !== previous.identity || hostFetch !== previousFetch) && !edgeChanged) void load()
})
onMounted(() => { document.addEventListener('fullscreenchange', fullscreenChanged); void load() })
onBeforeUnmount(() => { loadGeneration += 1; fullscreenGeneration += 1; loadController?.abort(); destroyGraph(); document.removeEventListener('fullscreenchange', fullscreenChanged) })
</script>

<template>
  <section ref="panel" class="kuery-panel k-card" :class="{ 'kuery-full': full }" aria-labelledby="topology-title">
    <div class="kuery-panel-head"><div><h2 id="topology-title" class="kuery-panel-title" tabindex="-1">Fleet topology</h2><p v-if="currentSnapshot" class="meta">The initial view groups resources by edge and namespace. Activate a resource node to show or hide its declared relationships. Use Impact to investigate deletion effects.</p></div><div v-if="currentSnapshot" class="kuery-view-switch" role="group" aria-label="Topology representation"><button v-for="value in ['graph','list']" :key="value" type="button" class="k-btn k-btn--ghost kuery-view-btn" :aria-pressed="representation === value" @click="representation = value as 'graph' | 'list'">{{ value === 'graph' ? 'Graph' : 'List' }}</button></div></div>
    <template v-if="loaded">
      <p id="topology-bounds" class="meta">View bounds: up to 1,000 members per edge; relation expansion stops at depth 5 and returns at most 200 objects for Namespace membership. Expand all stops at 4,000 nodes or 30 rounds. The displayed topology may be partial at these bounds.</p>
      <p v-if="currentSnapshot && representation === 'graph'" id="topology-help" class="meta">Focus the graph to pan with Arrow keys or W/A/S/D, zoom with +/−, enter full screen with F, and exit with Escape.</p>
      <div class="kuery-toolbar kuery-topology-controls">
        <label><span id="topology-layout-label">Layout</span><FormSelect v-model="layout" :options="layoutOptions" labelledby="topology-layout-label" /></label>
        <label><span id="topology-edge-label">Edge</span><FormSelect v-model="edge" :options="edgeOptions" labelledby="topology-edge-label" /></label>
        <label><span id="topology-kind-label">Kind</span><FormSelect v-model="kind" :options="kindOptions" labelledby="topology-kind-label" /></label>
        <label><span id="topology-namespace-label">Namespace</span><FormSelect v-model="namespace" :options="namespaceOptions" labelledby="topology-namespace-label" /></label>
        <button type="button" class="k-btn k-btn--ghost" :disabled="!currentSnapshot || !graph || expandingAll" @click="expandAll">{{ expandingAll ? 'Expanding…' : 'Expand all' }}</button>
        <button v-if="expandingAll" type="button" class="k-btn k-btn--ghost" @click="cancelExpandAll">Cancel expansion</button>
        <button v-if="layoutRunning" type="button" class="k-btn k-btn--ghost" @click="stopLayout">Stop layout</button>
        <button type="button" class="k-btn k-btn--ghost" :disabled="!currentSnapshot || !graph" @click="resetGraph">Reset graph</button>
        <button type="button" class="k-btn k-btn--ghost" @click="toggleFullscreen">{{ full ? 'Exit full screen' : 'Full screen' }}</button>
      </div>
    </template>
    <div v-if="loading && !currentSnapshot && !error && !notEngaged" class="kuery-read-state" role="status">Building fleet topology…</div>
    <div v-else-if="notEngaged && !currentSnapshot" class="kuery-read-state" role="status">
      <div class="kuery-error-copy">
        <span>Your Kubernetes edge is connected, but Kuery has not started syncing it yet.</span>
        <span class="meta">This can take a moment. If it continues, ask a workspace admin to confirm Kuery and Edges are enabled here.</span>
      </div>
      <button type="button" class="k-btn k-btn--ghost" :aria-disabled="loading || undefined" :aria-busy="loading || undefined" @click="retryLoad">{{ loading ? 'Checking…' : 'Check again' }}</button>
    </div>
    <div v-else-if="error && !currentSnapshot" class="kuery-read-state kuery-error" role="alert">
      <div class="kuery-error-copy">
        <span>{{ error }}</span>
        <details v-if="errorDetail" class="k-resource-technical">
          <summary class="k-resource-technical__summary">
            <span class="k-resource-technical__summary-label">Technical details</span>
            <ChevronDown class="k-resource-technical__chevron" :size="14" aria-hidden="true" />
          </summary>
          <div class="k-resource-technical__body">
            <section class="k-resource-technical__section">
              <h3 class="k-resource-technical__section-title">Topology query</h3>
              <div class="k-resource-technical__content"><pre class="k-resource-technical__pre">{{ errorDetail }}</pre></div>
            </section>
          </div>
        </details>
      </div>
      <button type="button" class="k-btn k-btn--ghost" :aria-disabled="loading || undefined" :aria-busy="loading || undefined" @click="retryLoad">{{ loading ? 'Retrying…' : 'Retry' }}</button>
    </div>
    <div v-else-if="currentSnapshot">
      <div v-if="notEngaged && loaded" class="kuery-warning" role="status">
        <span>The last successful topology is still shown, but Kuery is not currently syncing an edge in this workspace.</span>
        <button type="button" class="k-btn k-btn--ghost" :aria-disabled="loading || undefined" :aria-busy="loading || undefined" @click="retryLoad">{{ loading ? 'Checking…' : 'Check again' }}</button>
      </div>
      <div v-if="error" class="kuery-inline-error" role="alert">
        <div class="kuery-error-copy">
          <span>Could not refresh the topology. The last successful result is still shown.</span>
          <details v-if="errorDetail" class="k-resource-technical">
            <summary class="k-resource-technical__summary">
              <span class="k-resource-technical__summary-label">Technical details</span>
              <ChevronDown class="k-resource-technical__chevron" :size="14" aria-hidden="true" />
            </summary>
            <div class="k-resource-technical__body">
              <section class="k-resource-technical__section">
                <h3 class="k-resource-technical__section-title">Topology query</h3>
                <div class="k-resource-technical__content"><pre class="k-resource-technical__pre">{{ errorDetail }}</pre></div>
              </section>
            </div>
          </details>
        </div>
        <button type="button" class="k-btn k-btn--ghost" :aria-disabled="loading || undefined" :aria-busy="loading || undefined" @click="retryLoad">{{ loading ? 'Retrying…' : 'Retry' }}</button>
      </div>
      <div v-if="rows.length === 0" class="kuery-read-state" role="status">No clusters engaged. Connect a Kubernetes edge, then retry.</div>
      <div v-else-if="tree.edges.length === 0" class="kuery-read-state" role="status">No resources match the current topology filters.</div>
      <div v-else-if="representation === 'list'" class="kuery-topology-list" role="region" aria-label="Fleet topology list">
        <section v-for="edgeGroup in tree.edges" :key="edgeGroup.cluster" class="kuery-tree-edge"><h3>Edge <code>{{ edgeGroup.name }}</code></h3>
          <template v-for="group in edgeGroup.namespaces" :key="group.name"><h4>Namespace <code>{{ group.name }}</code></h4><button v-if="group.resource" type="button" class="kuery-topology-resource" @click="emit('inspect', group.resource.object)">{{ resourceLabel(group.resource.object) }}</button><button v-for="row in group.resources" :key="row.key" type="button" class="kuery-topology-resource" @click="emit('inspect', row.object)">{{ resourceLabel(row.object) }}</button></template>
          <template v-if="edgeGroup.resources.length"><h4>Cluster-scoped</h4><button v-for="row in edgeGroup.resources" :key="row.key" type="button" class="kuery-topology-resource" @click="emit('inspect', row.object)">{{ resourceLabel(row.object) }}</button></template>
        </section>
      </div>
      <div v-else>
        <div v-if="graphError" class="kuery-inline-error" role="alert">{{ graphError }} <button type="button" class="k-btn k-btn--ghost" :aria-disabled="graphRetrying || undefined" :aria-busy="graphRetrying || undefined" @click="retryGraph">{{ graphRetrying ? 'Retrying graph…' : 'Retry graph' }}</button></div>
        <div v-if="expansionFailure" class="kuery-inline-error" role="alert"><span>{{ expansionFailure.message }}</span><button type="button" class="k-btn k-btn--ghost" @click="retryExpansion">Retry expansion</button></div>
        <p class="kuery-sr-only" role="status" aria-live="polite" aria-atomic="true">{{ expansionStatus }}</p>
        <p v-if="layoutRunning" class="meta" role="status">Force layout is settling {{ layoutNodeCount.toLocaleString() }} nodes in the background; the graph stays interactive. Stop layout keeps the current positions.</p>
        <div ref="graphHost" class="kuery-graph" role="region" aria-label="Fleet topology visualization" aria-describedby="topology-help topology-bounds" tabindex="0" @keydown="graphKeydown" />
      </div>
    </div>
    <template v-if="currentSnapshot">
      <p v-if="incomplete" class="kuery-warning" role="status">Kuery reported response-level truncation for this query. That status does not identify relation-level bounds; the view limits above still apply.<span v-if="responseWarnings.length"> {{ responseWarnings.join(' ') }}</span></p>
      <p v-else-if="responseWarnings.length" class="kuery-warning" role="status">Kuery reported: {{ responseWarnings.join(' ') }} Relation-level bounds are listed above separately.</p>
      <p v-if="memberLimitReached" class="kuery-warning" role="status">At least one edge returned 1,000 members, its configured relation bound. Additional members may be omitted.</p>
      <p v-if="relationResponseTruncated" class="kuery-warning" role="status">Kuery reported response-level truncation during relation expansion. This does not identify which relation was bounded.</p>
      <p v-if="relationLimitReached" class="kuery-warning" role="status">Namespace membership reached its 200-object relation bound. Additional members may be omitted.</p>
      <p v-if="expansionBound === 'nodes'" class="kuery-warning" role="status">Expand all stopped at 4,000 graph nodes. The graph remains usable; expand individual nodes to inspect a narrower branch.</p>
      <p v-else-if="expansionBound === 'rounds'" class="kuery-warning" role="status">Expand all stopped after 30 rounds. The graph remains usable; expand individual nodes to inspect a narrower branch.</p>
    </template>
  </section>
</template>
