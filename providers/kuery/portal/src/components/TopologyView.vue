<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import type { FarosContext } from '../element'
import type { ObjectResult, QuerySpec, QueryStatus } from '../api'
import {
  buildTopologyElements, deriveTopologyTree, graphKeyAction, IMPACT_RELATIONS, mountGraph, relationElements, themeStyle,
  type GraphHandle,
} from '../graph'
import { createKueryRequestContext, errorMessage, resourceLabel, useKueryApi } from '../kuery'
import FormSelect from '../portalkit/FormSelect.vue'

const props = defineProps<{ context: FarosContext | null; edges: string[]; active: boolean }>()
const emit = defineEmits<{ inspect: [row: ObjectResult] }>()
const context = computed(() => props.context)
const { api, query } = useKueryApi(context)
const rows = ref<ObjectResult[]>([])
const loaded = ref(false)
const loading = ref(false)
const error = ref('')
const incomplete = ref(false)
const edge = ref('')
const kind = ref('')
const namespace = ref('')
const layout = ref<'breadthfirst' | 'concentric' | 'circle' | 'cose'>('breadthfirst')
const representation = ref<'graph' | 'list'>('graph')
const graphHost = ref<HTMLElement | null>(null)
const panel = ref<HTMLElement | null>(null)
const graphError = ref('')
const expansionFailure = ref<{ id: string | null; message: string } | null>(null)
const full = ref(false)
const expandingAll = ref(false)
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
  loadController = controller
  loading.value = true; error.value = ''
  const isCurrent = (): boolean => {
    if (loadController !== controller || loadGeneration !== requestGeneration) return false
    return requestContext.value.identity === requestIdentity
  }
  try {
    const result = await query(topologySpec(), controller.signal)
    if (!isCurrent()) return
    rows.value = result.objects ?? []; incomplete.value = !!result.incomplete; loaded.value = true
    responseWarnings.value = result.warnings ?? []
  } catch (reason) {
    if (!isCurrent()) return
    const message = errorMessage(reason, 'Retry the topology query or select one edge.')
    if (message) error.value = message
  } finally {
    if (isCurrent()) loading.value = false
  }
}

function layoutConfig(): Record<string, unknown> {
  if (layout.value === 'concentric') return { name: 'concentric', concentric: (node: { data: (key: string) => unknown }) => node.data('tier') === 'cluster' ? 3 : node.data('tier') === 'namespace' ? 2 : 1, levelWidth: () => 1, minNodeSpacing: 30, padding: 20 }
  if (layout.value === 'circle') return { name: 'circle', padding: 20 }
  if (layout.value === 'cose') return { name: 'cose', idealEdgeLength: 70, nodeRepulsion: 9000, padding: 20, animate: false }
  return { name: 'breadthfirst', directed: true, spacingFactor: 1, padding: 20 }
}

function destroyGraph(): void {
  graphGeneration += 1
  graphController?.abort()
  graphController = null
  graph?.destroy()
  graph = null
  graphObjects.clear()
  expansionRequests.clear()
  relationResponseTruncated.value = false
  relationLimitReached.value = false
  expansionBound.value = null
  expandingAll.value = false
}

async function mount(): Promise<void> {
  destroyGraph(); graphError.value = ''; expansionFailure.value = null
  if (!graphHost.value || representation.value !== 'graph' || tree.value.edges.length === 0) return
  const generation = graphGeneration
  graphController = new AbortController()
  const built = buildTopologyElements(rows.value, { kind: kind.value, namespace: namespace.value })
  graphObjects = new Map(Object.entries(built.nodeIndex))
  try {
    const handle = await mountGraph(graphHost.value, built.elements, themeStyle(graphHost.value), id => { if (generation === graphGeneration) void expand(id) }, `${(context.value?.basePath || '').replace(/\/?$/, '/')}cytoscape.min.js`, layoutConfig())
    if (generation !== graphGeneration) return handle.destroy()
    graph = handle
    requestAnimationFrame(() => { if (generation === graphGeneration && graph === handle) handle.fit() })
  } catch (reason) {
    if (generation === graphGeneration) graphError.value = errorMessage(reason, 'Retry the graph or use the List representation.')
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
  if (!handle || !row) return
  if (handle.isExpanded(id)) {
    expansionRequests.set(id, ++expansionGeneration)
    handle.collapseFrom(id)
    return
  }
  const requestGeneration = ++expansionGeneration
  expansionRequests.set(id, requestGeneration)
  const isCurrent = () => graph === handle && graphGeneration === currentGraphGeneration && graphController === controller && expansionRequests.get(id) === requestGeneration
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
    handle.relayout(layoutConfig())
  } catch (reason) {
    if (!isCurrent()) return
    handle.markExpanded(id, false)
    if (reason instanceof DOMException && reason.name === 'AbortError') return
    expansionFailure.value = { id, message: errorMessage(reason, 'Retry this node expansion; the current graph is unchanged.') }
  }
}

async function expandAll(): Promise<void> {
  const handle = graph; const controller = graphController; const currentGraphGeneration = graphGeneration
  if (!handle || expandingAll.value) return
  const isCurrent = () => graph === handle && graphGeneration === currentGraphGeneration && graphController === controller
  expandingAll.value = true
  let rounds = 0
  try {
    for (; rounds < 30 && isCurrent() && handle.nodeCount() < 4000; rounds += 1) {
      const ids = [...graphObjects.keys()].filter(id => handle.hasNode(id) && !handle.isExpanded(id)).slice(0, 300)
      if (!ids.length) break
      const status = await query({ maxDepth: 5, limit: ids.length, filter: { objects: ids.map(id => ({ id })) }, objects: { id: true, cluster: true, relations: impactRelations() } }, controller?.signal)
      let added = 0
      if (!isCurrent()) break
      recordRelationStatus(status)
      for (const object of status.objects ?? []) {
        if (!object.id) continue
        const built = relationElements(object.id, object); added += handle.add(built.elements).length
        for (const [key, row] of Object.entries(built.nodeIndex)) graphObjects.set(key, row)
      }
      ids.forEach(id => handle.markExpanded(id, true)); handle.relayout(layoutConfig())
      if (added === 0) break
    }
    if (isCurrent()) {
      if (handle.nodeCount() >= 4000) expansionBound.value = 'nodes'
      else if (rounds >= 30) expansionBound.value = 'rounds'
    }
  } catch (reason) {
    if (!isCurrent()) return
    if (reason instanceof DOMException && reason.name === 'AbortError') return
    const message = errorMessage(reason, 'Retry Expand all; the graph remains usable.')
    if (message) expansionFailure.value = { id: null, message }
  } finally { if (isCurrent()) expandingAll.value = false }
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

watch(edge, () => void load())
watch([rows, kind, namespace, representation], () => nextTick(() => void mount()), { deep: false })
watch(layout, () => graph ? graph.relayout(layoutConfig()) : nextTick(() => void mount()))
watch(() => context.value?.theme, () => { if (graph && graphHost.value) graph.restyle(themeStyle(graphHost.value)) })
watch(() => props.active, active => { if (active) requestAnimationFrame(() => graph?.fit()) })
watch([api, requestContext], ([ready, current], [wasReady, previous]) => {
  if (ready && (!wasReady || current.identity !== previous.identity)) void load()
})
onMounted(() => { document.addEventListener('fullscreenchange', fullscreenChanged); void load() })
onBeforeUnmount(() => { loadGeneration += 1; fullscreenGeneration += 1; loadController?.abort(); destroyGraph(); document.removeEventListener('fullscreenchange', fullscreenChanged) })
</script>

<template>
  <section ref="panel" class="kuery-panel k-card" :class="{ 'kuery-full': full }" aria-labelledby="topology-title">
    <div class="kuery-panel-head"><div><h2 id="topology-title" class="kuery-panel-title">Fleet topology</h2><p class="meta">Activate a graph node to expand it, then activate it again to collapse. A→B means deleting A impacts B.</p></div><div class="kuery-view-switch" role="group" aria-label="Topology representation"><button v-for="value in ['graph','list']" :key="value" type="button" class="k-btn k-btn--ghost kuery-view-btn" :aria-pressed="representation === value" @click="representation = value as 'graph' | 'list'">{{ value === 'graph' ? 'Graph' : 'List' }}</button></div></div>
    <p id="topology-bounds" class="meta">View bounds: up to 1,000 members per edge; relation expansion stops at depth 5 and returns at most 200 objects for Namespace membership. Expand all stops at 4,000 nodes or 30 rounds. The displayed topology may be partial at these bounds.</p>
    <p v-if="representation === 'graph'" id="topology-help" class="meta">Focus the graph to pan with Arrow keys or W/A/S/D, zoom with +/−, enter full screen with F, and exit with Escape.</p>
    <div class="kuery-toolbar kuery-topology-controls">
      <label><span id="topology-layout-label">Layout</span><FormSelect v-model="layout" :options="layoutOptions" labelledby="topology-layout-label" /></label>
      <label><span id="topology-edge-label">Edge</span><FormSelect v-model="edge" :options="edgeOptions" labelledby="topology-edge-label" /></label>
      <label><span id="topology-kind-label">Kind</span><FormSelect v-model="kind" :options="kindOptions" labelledby="topology-kind-label" /></label>
      <label><span id="topology-namespace-label">Namespace</span><FormSelect v-model="namespace" :options="namespaceOptions" labelledby="topology-namespace-label" /></label>
      <button type="button" class="k-btn k-btn--ghost" :disabled="!graph || expandingAll" @click="expandAll">{{ expandingAll ? 'Expanding…' : 'Expand all' }}</button>
      <button type="button" class="k-btn k-btn--ghost" :disabled="!graph" @click="resetGraph">Reset graph</button>
      <button type="button" class="k-btn k-btn--ghost" @click="toggleFullscreen">{{ full ? 'Exit full screen' : 'Full screen' }}</button>
    </div>
    <div v-if="loading && !loaded" class="kuery-read-state" role="status">Building fleet topology…</div>
    <div v-else-if="error && !loaded" class="kuery-read-state kuery-error" role="alert">{{ error }} <button type="button" class="k-btn k-btn--ghost" @click="load">Retry</button></div>
    <div v-else>
      <div v-if="error" class="kuery-inline-error" role="alert">Showing the last successful topology. {{ error }} <button type="button" class="k-btn k-btn--ghost" @click="load">Retry</button></div>
      <div v-if="rows.length === 0" class="kuery-read-state" role="status">No clusters engaged. Connect a Kubernetes edge, then retry.</div>
      <div v-else-if="tree.edges.length === 0" class="kuery-read-state" role="status">No resources match the current topology filters.</div>
      <div v-else-if="representation === 'list'" class="kuery-topology-list" role="region" aria-label="Fleet topology list">
        <section v-for="edgeGroup in tree.edges" :key="edgeGroup.cluster" class="kuery-tree-edge"><h3>Edge <code>{{ edgeGroup.name }}</code></h3>
          <template v-for="group in edgeGroup.namespaces" :key="group.name"><h4>Namespace <code>{{ group.name }}</code></h4><button v-if="group.resource" type="button" class="kuery-topology-resource" @click="emit('inspect', group.resource.object)">{{ resourceLabel(group.resource.object) }}</button><button v-for="row in group.resources" :key="row.key" type="button" class="kuery-topology-resource" @click="emit('inspect', row.object)">{{ resourceLabel(row.object) }}</button></template>
          <template v-if="edgeGroup.resources.length"><h4>Cluster-scoped</h4><button v-for="row in edgeGroup.resources" :key="row.key" type="button" class="kuery-topology-resource" @click="emit('inspect', row.object)">{{ resourceLabel(row.object) }}</button></template>
        </section>
      </div>
      <div v-else><div v-if="graphError" class="kuery-inline-error" role="alert">{{ graphError }} <button type="button" class="k-btn k-btn--ghost" @click="mount">Retry graph</button></div><div v-if="expansionFailure" class="kuery-inline-error" role="alert"><span>{{ expansionFailure.message }}</span><button type="button" class="k-btn k-btn--ghost" @click="retryExpansion">Retry expansion</button></div><div ref="graphHost" class="kuery-graph" role="region" aria-label="Fleet topology graph" aria-describedby="topology-help topology-bounds" tabindex="0" @keydown="graphKeydown" /></div>
    </div>
    <p v-if="incomplete" class="kuery-warning" role="status">Kuery reported response-level truncation for this query. That status does not identify relation-level bounds; the view limits above still apply.<span v-if="responseWarnings.length"> {{ responseWarnings.join(' ') }}</span></p>
    <p v-else-if="responseWarnings.length" class="kuery-warning" role="status">Kuery reported: {{ responseWarnings.join(' ') }} Relation-level bounds are listed above separately.</p>
    <p v-if="memberLimitReached" class="kuery-warning" role="status">At least one edge returned 1,000 members, its configured relation bound. Additional members may be omitted.</p>
    <p v-if="relationResponseTruncated" class="kuery-warning" role="status">Kuery reported response-level truncation during relation expansion. This does not identify which relation was bounded.</p>
    <p v-if="relationLimitReached" class="kuery-warning" role="status">Namespace membership reached its 200-object relation bound. Additional members may be omitted.</p>
    <p v-if="expansionBound === 'nodes'" class="kuery-warning" role="status">Expand all stopped at 4,000 graph nodes. The graph remains usable; expand individual nodes to inspect a narrower branch.</p>
    <p v-else-if="expansionBound === 'rounds'" class="kuery-warning" role="status">Expand all stopped after 30 rounds. The graph remains usable; expand individual nodes to inspect a narrower branch.</p>
  </section>
</template>
