// Cytoscape-backed impact graph for the kuery provider.
//
// Cytoscape (~145 kB gzip) is loaded LAZILY, only when someone opens the
// graph view — the inventory table never pays for it. We can't use a bundler
// dynamic import() for this: the portal build is IIFE library mode (see
// vite.config.ts — the script tag runs before any module loader), and an
// IIFE bundle is self-contained, so Rollup would inline the import into
// main.js. Instead we vendor cytoscape.min.js as a static asset (copied into
// dist/ by the build script) and inject it via a <script> tag on first use,
// reading the window.cytoscape global it defines. `import type` below is
// erased at build time, so this module pulls in zero Cytoscape bytes.

import type cytoscape from 'cytoscape'
import type { ObjectResult } from './api'

// The UMD bundle assigns this global.
declare global {
  interface Window {
    cytoscape?: typeof cytoscape
  }
}

// Impact direction per relation, from the anchor's point of view. An edge is
// always drawn so the arrow means "deleting source impacts target":
//   - 'up'   : the related object is UPSTREAM — deleting it impacts the anchor
//              (anchor depends on it). Drawn related → anchor. e.g. a Pod's
//              owners, references, the namespace it lives in.
//   - 'down' : the related object is DOWNSTREAM — deleting the anchor impacts
//              it (blast radius). Drawn anchor → related. e.g. a Deployment's
//              descendants, a Namespace's members, who selects the anchor.
//   - 'lateral': peer association with no clear deletion direction.
// This is what keeps a Namespace as a Pod's PARENT (it impacts the pod), not a
// child, splitting the graph into "impacted-by" vs "impacts" branches.
//
// Mirror of kuery's engine.RelationDirections (pkg/engine/relations.go) — the
// authority. Keep in lockstep: up = engine "upstream", down = "downstream".
export type RelDir = 'up' | 'down' | 'lateral'

/**
 * The one relation vocabulary shared by graph styling, list labels, and the
 * impact legend. Keeping color, direction, and explanatory text together
 * prevents a graph edge from acquiring a meaning the legend does not explain.
 */
export interface RelationMetadata {
  name: string
  label: string
  legendLabel: string
  color: string
  lightColor: string
  lineStyle: 'solid' | 'dashed' | 'dotted'
  direction: RelDir
  description: string
}

export const RELATION_METADATA: readonly RelationMetadata[] = [
  {
    name: 'owners', label: 'owners', legendLabel: 'Owners', color: '#e0b34f', lightColor: '#715100', lineStyle: 'solid', direction: 'up',
    description: 'Upstream - deleting the related object impacts this object.',
  },
  {
    name: 'descendants+', label: 'descendants', legendLabel: 'Descendants', color: '#4f9be0', lightColor: '#075b9e', lineStyle: 'dashed', direction: 'down',
    description: 'Downstream - deleting this object impacts the related object.',
  },
  {
    name: 'references', label: 'references', legendLabel: 'References', color: '#9b6de0', lightColor: '#5f3bb8', lineStyle: 'dotted', direction: 'up',
    description: 'Upstream - deleting the related object impacts this object.',
  },
  {
    name: 'selects', label: 'selects', legendLabel: 'Selects', color: '#4fe0a8', lightColor: '#006b4d', lineStyle: 'dashed', direction: 'up',
    description: 'Upstream - deleting the related object impacts this object.',
  },
  {
    name: 'selected-by', label: 'selected-by', legendLabel: 'Selected by', color: '#e07a4f', lightColor: '#923b13', lineStyle: 'dotted', direction: 'down',
    description: 'Downstream - deleting this object impacts the related object.',
  },
  {
    name: 'linked+', label: 'linked', legendLabel: 'Linked', color: '#e0519b', lightColor: '#96205d', lineStyle: 'dashed', direction: 'lateral',
    description: 'Lateral - no deletion direction is implied.',
  },
  {
    name: 'grouped', label: 'grouped', legendLabel: 'Grouped', color: '#8a93a8', lightColor: '#4d5568', lineStyle: 'dotted', direction: 'lateral',
    description: 'Lateral - no deletion direction is implied.',
  },
  {
    name: 'namespace', label: 'namespace', legendLabel: 'Namespace', color: '#5fae7a', lightColor: '#24683f', lineStyle: 'solid', direction: 'up',
    description: 'Upstream - deleting the related object impacts this object.',
  },
  {
    name: 'namespaced', label: 'contains', legendLabel: 'Contains', color: '#5fae7a', lightColor: '#24683f', lineStyle: 'dashed', direction: 'down',
    description: 'Downstream - deleting this object impacts the related object.',
  },
]

// These derived maps remain exported for callers that need O(1) lookup, while
// RELATION_METADATA is the source of truth for the graph and the legend.
export const IMPACT_RELATIONS: readonly string[] = RELATION_METADATA.map(({ name }) => name)
export const RELATION_COLORS: Record<string, string> = Object.fromEntries(
  RELATION_METADATA.map(({ name, color }) => [name, color]),
)
export const RELATION_LABELS: Record<string, string> = Object.fromEntries(
  RELATION_METADATA.map(({ name, label }) => [name, label]),
)

export function relationColor(relation: RelationMetadata, light: boolean): string {
  return light ? relation.lightColor : relation.color
}

function relationLabel(rel: string): string {
  return RELATION_LABELS[rel] ?? rel
}
export const RELATION_DIR: Record<string, RelDir> = {
  ...Object.fromEntries(RELATION_METADATA.map(({ name, direction }) => [name, direction])),
  // The non-transitive spellings are accepted by the engine and remain useful
  // when rendering a custom QuerySpec outside the built-in impact query.
  descendants: 'down',
  linked: 'lateral',
}

// orientEdge returns [source, target] for an edge between the anchor and a
// related node, per the relation's impact direction (see RELATION_DIR).
function orientEdge(anchorId: string, relatedId: string, rel: string): [string, string] {
  return (RELATION_DIR[rel] ?? 'down') === 'up' ? [relatedId, anchorId] : [anchorId, relatedId]
}

function relationEdgeId(source: string, target: string, rel: string): string {
  return `${source}>${target}:${rel}`
}

export interface BuildResult {
  elements: cytoscape.ElementDefinition[]
  // nodeId → the source ObjectResult, so a node tap can re-anchor the graph
  // by re-running impact on the tapped object (which needs kind/apiVersion/
  // metadata/cluster, not just the id).
  nodeIndex: Record<string, ObjectResult>
}

// buildElements turns one impact ObjectResult (anchor + relations keyed by
// type) into a Cytoscape node/edge set. Nodes are deduped by id — an object
// that is both, say, an owner and a reference becomes a single node wearing
// two colored edges rather than two stacked nodes.
//
// Transitive relations (descendants+, linked+) arrive pre-flattened from the
// API, so their members hang directly off the anchor here; tapping one
// re-anchors to walk the true multi-hop chain.
export function buildElements(anchor: ObjectResult): BuildResult {
  const elements: cytoscape.ElementDefinition[] = []
  const nodeIndex: Record<string, ObjectResult> = {}
  const seen = new Set<string>()
  const relationEdges = new Set<string>()

  const anchorId = anchor.id || 'anchor'
  pushNode(elements, nodeIndex, seen, anchorId, anchor, true)

  const rels = anchor.relations ?? {}
  for (const [rel, items] of Object.entries(rels)) {
    ;(items ?? []).forEach((it, i) => {
      const id = it.id || `${rel}:${i}`
      pushNode(elements, nodeIndex, seen, id, it, false)
      const [source, target] = orientEdge(anchorId, id, rel)
      const edgeId = relationEdgeId(source, target, rel)
      if (relationEdges.has(edgeId)) return
      relationEdges.add(edgeId)
      elements.push({ data: { id: edgeId, source, target, rel, edgeLabel: relationLabel(rel) } })
    })
  }
  return { elements, nodeIndex }
}

export interface TopologyResourceRow {
  // Stable key used by the accessible list to route activation back to the
  // exact ObjectResult. The API id is preferred, with a deterministic fallback
  // for older sync records that did not include one.
  key: string
  object: ObjectResult
  edge: string
  namespace: string
  kind: string
  name: string
}

export interface TopologyNamespaceGroup {
  key: string
  name: string
  // A synced Namespace is the actual tier node in the graph. The list keeps
  // that same object available as an inspectable resource button without
  // duplicating it as a child row.
  resource?: TopologyResourceRow
  resources: TopologyResourceRow[]
}

export interface TopologyEdgeGroup {
  key: string
  // Full cluster key, including the tenant prefix. Graph node ids use this so
  // two edges with the same display name cannot collide.
  cluster: string
  name: string
  namespaces: TopologyNamespaceGroup[]
  resources: TopologyResourceRow[]
}

export interface TopologyTree {
  edges: TopologyEdgeGroup[]
}

function topologyResource(
  object: ObjectResult,
  edge: string,
  clusterKey: string,
): TopologyResourceRow {
  const metadata = object.object?.metadata ?? {}
  const kind = object.object?.kind || '?'
  const name = metadata.name || '?'
  const namespace = metadata.namespace || ''
  return {
    key: object.id || `${clusterKey}/${kind}/${namespace}/${name}`,
    object,
    edge,
    namespace,
    kind,
    name,
  }
}

// deriveTopologyTree is the pointer-free representation of the same topology
// query used by buildTopologyElements. It is deliberately pure: filters are
// applied to the cached API result and the returned Edge → Namespace → Resource
// hierarchy carries only rows that are actually visible in that view.
//
// A Namespace object is the real namespace tier (and remains an inspectable
// resource button in the list). When no Namespace object was synced, a
// namespace group is synthesized from a namespaced member, matching the graph
// fallback. Cluster-scoped members remain directly under their Edge.
export function deriveTopologyTree(
  clusters: ObjectResult[],
  opts?: { kind?: string; namespace?: string },
): TopologyTree {
  const wantKind = opts?.kind || ''
  const wantNs = opts?.namespace || ''
  const edges: TopologyEdgeGroup[] = []

  clusters.forEach((cluster, clusterIndex) => {
    const clusterName = cluster.cluster || cluster.object?.metadata?.name || 'cluster'
    const edge = edgeOf(clusterName)
    const edgeKey = `${clusterName}:${clusterIndex}`
    const edgeGroup: TopologyEdgeGroup = { key: edgeKey, cluster: clusterName, name: edge, namespaces: [], resources: [] }
    const namespaceByName = new Map<string, TopologyNamespaceGroup>()
    const members = cluster.relations?.members ?? []

    const ensureNamespace = (name: string): TopologyNamespaceGroup => {
      const existing = namespaceByName.get(name)
      if (existing) return existing
      const group: TopologyNamespaceGroup = { key: `${edgeKey}/namespace/${name}`, name, resources: [] }
      namespaceByName.set(name, group)
      edgeGroup.namespaces.push(group)
      return group
    }

    // Preserve the graph's rule that real Namespace objects establish visible
    // namespace tiers before ordinary members are attached to them.
    members.forEach((member) => {
      const object = member.object ?? {}
      if (object.kind !== 'Namespace') return
      const name = object.metadata?.name || ''
      if (!name || (wantKind && wantKind !== 'Namespace') || (wantNs && wantNs !== name)) return
      const group = ensureNamespace(name)
      group.resource = topologyResource(member, edge, clusterName)
    })

    members.forEach((member) => {
      const object = member.object ?? {}
      const kind = object.kind || '?'
      if (kind === 'Namespace') return
      const namespace = object.metadata?.namespace || ''
      if ((wantKind && wantKind !== kind) || (wantNs && wantNs !== namespace)) return
      const row = topologyResource(member, edge, clusterName)
      if (namespace) ensureNamespace(namespace).resources.push(row)
      else edgeGroup.resources.push(row)
    })

    // A cluster with no matching rows should not produce an empty Edge item in
    // the list. The graph also only creates namespace tiers when it has a row.
    if (edgeGroup.resources.length || edgeGroup.namespaces.length) edges.push(edgeGroup)
  })

  return { edges }
}

// buildTopologyElements turns a clusters-rooted query result (each cluster
// with its `members` relation) into a fleet tree: Edge → Namespace → object,
// with cluster-scoped objects hanging straight off the Edge. Structural edges
// all use rel "namespace" so they share the containment color. Both graph and
// list views consume deriveTopologyTree so filtering cannot make the two drift.
export function buildTopologyElements(
  clusters: ObjectResult[],
  opts?: { kind?: string; namespace?: string },
): BuildResult {
  const elements: cytoscape.ElementDefinition[] = []
  const nodeIndex: Record<string, ObjectResult> = {}
  const nodes = new Set<string>()
  const edges = new Set<string>()

  const addNode = (id: string, data: Record<string, unknown>) => {
    if (nodes.has(id)) return
    nodes.add(id)
    elements.push({ data: { id, ...data } })
  }
  const addEdge = (source: string, target: string, rel = 'namespace') => {
    const id = relationEdgeId(source, target, rel)
    if (edges.has(id)) return
    edges.add(id)
    elements.push({ data: { id, source, target, rel } })
  }

  for (const edgeGroup of deriveTopologyTree(clusters, opts).edges) {
    const cid = `cluster:${edgeGroup.cluster}`
    addNode(cid, { label: edgeGroup.name, tier: 'cluster', anchor: 'true', kind: 'Cluster', name: edgeGroup.name })

    for (const namespaceGroup of edgeGroup.namespaces) {
      const namespaceId = namespaceGroup.resource?.object.id || `ns:${edgeGroup.cluster}/${namespaceGroup.name}`
      addNode(namespaceId, { label: namespaceGroup.name, tier: 'namespace', kind: 'Namespace', name: namespaceGroup.name })
      if (namespaceGroup.resource) nodeIndex[namespaceId] = namespaceGroup.resource.object
      addEdge(cid, namespaceId)
      for (const row of namespaceGroup.resources) {
        addNode(row.key, { label: `${row.kind}\n${row.name}`, tier: 'object', kind: row.kind, name: row.name, edge: row.edge })
        nodeIndex[row.key] = row.object
        addEdge(namespaceId, row.key)
      }
    }

    for (const row of edgeGroup.resources) {
      addNode(row.key, { label: `${row.kind}\n${row.name}`, tier: 'object', kind: row.kind, name: row.name, edge: row.edge })
      nodeIndex[row.key] = row.object
      addEdge(cid, row.key)
    }
  }
  return { elements, nodeIndex }
}

function pushNode(
  elements: cytoscape.ElementDefinition[],
  nodeIndex: Record<string, ObjectResult>,
  seen: Set<string>,
  id: string,
  o: ObjectResult,
  anchor: boolean,
): void {
  nodeIndex[id] = o
  if (seen.has(id)) return
  seen.add(id)
  const obj = o.object ?? {}
  const kind = obj.kind || '?'
  elements.push({
    data: {
      id,
      label: `${kind}\n${shortName(o)}`,
      anchor: anchor ? 'true' : 'false',
      kind,
      name: shortName(o),
      edge: edgeOf(o.cluster),
    },
  })
}

// themeStyle snapshots the portal's CSS custom properties (Cytoscape draws
// to a canvas and cannot read CSS vars) into a Cytoscape stylesheet so the
// graph tracks the portal's light/dark palette.
export function themeStyle(host: Element): cytoscape.StylesheetStyle[] {
  const cs = getComputedStyle(host)
  const v = (name: string, fallback: string) => cs.getPropertyValue(name).trim() || fallback

  const surface = v('--color-surface-overlay', 'rgba(127,127,127,0.16)')
  const border = v('--color-border-default', 'rgba(127,127,127,0.45)')
  const text = v('--color-text-primary', '#e9e9f2')
  const muted = v('--color-text-muted', '#5d5f78')
  const accent = v('--color-accent', '#8b6bff')
  const light = typeof document !== 'undefined' && document.documentElement.classList.contains('light')

  const style: cytoscape.StylesheetStyle[] = [
    {
      selector: 'node',
      style: {
        'background-color': surface,
        'border-color': border,
        'border-width': 1,
        label: 'data(label)',
        color: text,
        'font-size': 9,
        'text-wrap': 'wrap',
        'text-max-width': '110px',
        'text-valign': 'bottom',
        'text-margin-y': 4,
        width: 24,
        height: 24,
        shape: 'round-rectangle',
      },
    },
    {
      selector: 'node[anchor = "true"]',
      style: {
        'background-color': accent,
        'border-color': accent,
        color: text,
        width: 38,
        height: 38,
        'font-size': 11,
        'font-weight': 700,
      },
    },
    // Topology tiers: the Edge (cluster) anchors as a hexagon; Namespaces are
    // muted diamonds; objects keep the default round-rectangle.
    {
      selector: 'node[tier = "cluster"]',
      style: { shape: 'hexagon', width: 44, height: 44 },
    },
    {
      selector: 'node[tier = "namespace"]',
      style: { shape: 'diamond', 'background-color': muted, 'border-color': border, width: 30, height: 30, 'font-size': 9 },
    },
    // Drilled nodes get an accent ring so you can see how far the net is
    // already expanded vs. what's still collapsed.
    {
      selector: 'node[expanded = "true"]',
      style: { 'border-color': accent, 'border-width': 3 },
    },
    {
      selector: 'node:active',
      style: { 'overlay-color': accent, 'overlay-opacity': 0.15 },
    },
    {
      selector: 'edge',
      style: {
        width: 1.5,
        'curve-style': 'bezier',
        'target-arrow-shape': 'triangle',
        'arrow-scale': 0.8,
        'line-color': muted,
        'target-arrow-color': muted,
        label: 'data(edgeLabel)',
        color: text,
        'font-size': 8,
        'text-rotation': 'autorotate',
        'text-background-color': surface,
        'text-background-opacity': 0.92,
        'text-background-padding': '2px',
      },
    },
  ]
  for (const relation of RELATION_METADATA) {
    const color = relationColor(relation, light)
    style.push({
      selector: `edge[rel = "${relation.name}"]`,
      style: {
        'line-color': color,
        'target-arrow-color': color,
        'line-style': relation.lineStyle,
      },
    })
  }
  return style
}

export interface GraphHandle {
  destroy(): void
  // add merges new nodes/edges into the live graph, skipping ids already
  // present (so an object reached from two parents becomes one node with two
  // edges — the real dependency net). Returns the elements actually added.
  add(elements: cytoscape.ElementDefinition[], nodeLimit?: number): cytoscape.ElementDefinition[]
  hasNode(id: string): boolean
  // markExpanded flags a node as already drilled so the UI can style it and
  // skip re-querying. collapseFrom removes the subtree reachable only through
  // the given node (leaves shared nodes alone).
  markExpanded(id: string, expanded: boolean): void
  isExpanded(id: string): boolean
  collapseFrom(id: string): void
  // relayout re-runs the layout after the graph grows/shrinks.
  relayout(layout?: Record<string, unknown>): void
  // Viewport controls for keyboard nav / fullscreen.
  panBy(dx: number, dy: number): void
  zoomBy(factor: number): void
  fit(): void
  restyle(style: cytoscape.StylesheetStyle[]): void
  nodeCount(): number
}

export type GraphKeyAction = 'pan-up' | 'pan-down' | 'pan-left' | 'pan-right' | 'zoom-in' | 'zoom-out' | 'fullscreen' | 'escape'

// Keep keyboard routing deterministic and separate from Cytoscape so the
// portal can prove that global arrow keys remain untouched until the graph is
// the active focus owner.
export function graphKeyAction(key: string): GraphKeyAction | null {
  switch (key) {
    case 'ArrowUp': case 'w': case 'W': return 'pan-up'
    case 'ArrowDown': case 's': case 'S': return 'pan-down'
    case 'ArrowLeft': case 'a': case 'A': return 'pan-left'
    case 'ArrowRight': case 'd': case 'D': return 'pan-right'
    case '+': case '=': return 'zoom-in'
    case '-': case '_': return 'zoom-out'
    case 'f': case 'F': return 'fullscreen'
    case 'Escape': return 'escape'
    default: return null
  }
}

export function graphOwnsFocus(graph: Element | null, activeElement: Element | null): boolean {
  return !!graph && (graph === activeElement || (!!activeElement && graph.contains(activeElement)))
}

// relationElements builds the child nodes/edges for one already-placed node
// (anchorId) from an impact result's relations. It does NOT emit the anchor
// itself. Child node ids are the objects' stable kuery ids, so re-expanding or
// reaching the same object from elsewhere dedupes to a single node. Edge ids
// are per (parent, rel, child) so parallel relations stay distinct.
export function relationElements(anchorId: string, anchor: ObjectResult): BuildResult {
  const elements: cytoscape.ElementDefinition[] = []
  const nodeIndex: Record<string, ObjectResult> = {}
  const relationEdges = new Set<string>()
  const rels = anchor.relations ?? {}
  for (const [rel, items] of Object.entries(rels)) {
    ;(items ?? []).forEach((it, i) => {
      const id = it.id || `${anchorId}:${rel}:${i}`
      const o = it.object ?? {}
      const kind = o.kind || '?'
      elements.push({
        data: { id, label: `${kind}\n${shortName(it)}`, tier: 'object', kind, name: shortName(it), edge: edgeOf(it.cluster) },
      })
      nodeIndex[id] = it
      // Orient by impact direction: upstream relations point INTO the anchor
      // (related → anchor), downstream out of it. Relation-qualified edge ids
      // dedupe repeated copies of one relation while keeping parallel
      // relations between the same pair visible.
      const [source, target] = orientEdge(anchorId, id, rel)
      const edgeId = relationEdgeId(source, target, rel)
      if (relationEdges.has(edgeId)) return
      relationEdges.add(edgeId)
      elements.push({ data: { id: edgeId, source, target, rel, edgeLabel: relationLabel(rel), expandedFrom: anchorId } })
    })
  }
  return { elements, nodeIndex }
}

let _libPromise: Promise<typeof cytoscape> | null = null

// loadCytoscape injects the vendored UMD bundle once and resolves to the
// window.cytoscape global. Concurrent callers share one in-flight load.
function loadCytoscape(libUrl: string): Promise<typeof cytoscape> {
  if (window.cytoscape) return Promise.resolve(window.cytoscape)
  if (_libPromise) return _libPromise
  _libPromise = new Promise((resolve, reject) => {
    const s = document.createElement('script')
    s.src = libUrl
    s.async = true
    s.onload = () =>
      window.cytoscape ? resolve(window.cytoscape) : reject(new Error('cytoscape global missing after load'))
    s.onerror = () => {
      _libPromise = null // allow a retry on the next graph open
      reject(new Error(`failed to load ${libUrl}`))
    }
    document.head.appendChild(s)
  })
  return _libPromise
}

// mountGraph lazy-loads Cytoscape (from libUrl) and renders the impact graph
// into container. onNodeTap fires for non-anchor nodes (the anchor is already
// centered) so the caller can re-anchor. Returns a handle whose destroy()
// tears down the instance and its listeners.
export async function mountGraph(
  container: HTMLElement,
  elements: cytoscape.ElementDefinition[],
  style: cytoscape.StylesheetStyle[],
  onNodeTap: (id: string) => void,
  libUrl: string,
  // Loose object so callers can pass any built-in layout config (tree, radial,
  // circle, force) without importing Cytoscape's layout union; cast below.
  layout?: Record<string, unknown>,
): Promise<GraphHandle> {
  const cytoscape = await loadCytoscape(libUrl)
  const cy = cytoscape({
    container,
    elements,
    style,
    layout: (layout ?? {
      name: 'concentric',
      concentric: (node: cytoscape.NodeSingular) => (node.data('anchor') === 'true' ? 2 : 1),
      levelWidth: () => 1,
      minNodeSpacing: 34,
      padding: 16,
    }) as unknown as cytoscape.LayoutOptions,
    // Allow zooming far out so a fully-expanded net still fits on screen.
    minZoom: 0.02,
    maxZoom: 3,
  })
  // Every node is tappable; the caller decides what (if anything) to do — for
  // the explorer that's expand/collapse, for the impact view it re-anchors.
  cy.on('tap', 'node', (evt: cytoscape.EventObject) => onNodeTap(evt.target.id()))
  // Cytoscape's canvas is not itself a keyboard control. Let a pointer user
  // opt into the same graph shortcuts without making the shortcuts global.
  const focusGraph = () => container.focus()
  container.addEventListener('pointerdown', focusGraph)
  let resizeFrame = 0
  const resizeObserver = typeof ResizeObserver === 'undefined'
    ? null
    : new ResizeObserver(() => {
        cancelAnimationFrame(resizeFrame)
        resizeFrame = requestAnimationFrame(() => cy.resize())
      })
  resizeObserver?.observe(container)

  return {
    destroy: () => {
      resizeObserver?.disconnect()
      if (resizeObserver) cancelAnimationFrame(resizeFrame)
      container.removeEventListener('pointerdown', focusGraph)
      cy.destroy()
    },
    hasNode: (id) => cy.getElementById(id).nonempty(),
    isExpanded: (id) => cy.getElementById(id).data('expanded') === 'true',
    markExpanded: (id, expanded) => {
      const n = cy.getElementById(id)
      if (n.nonempty()) n.data('expanded', expanded ? 'true' : 'false')
    },
    add: (els, nodeLimit = Number.POSITIVE_INFINITY) => {
      const acceptedNodes = new Set<string>()
      let nodeCount = cy.nodes().length
      const fresh = els.filter((e) => {
        const id = e.data?.id as string | undefined
        if (!id || cy.getElementById(id).nonempty()) return false
        const source = e.data?.source as string | undefined
        const target = e.data?.target as string | undefined
        if (source && target) {
          const hasEndpoint = (nodeID: string) => cy.getElementById(nodeID).nonempty() || acceptedNodes.has(nodeID)
          return hasEndpoint(source) && hasEndpoint(target)
        }
        if (acceptedNodes.has(id)) return false
        if (nodeCount >= nodeLimit) return false
        acceptedNodes.add(id)
        nodeCount += 1
        return true
      })
      if (fresh.length) cy.add(fresh)
      return fresh
    },
    collapseFrom: (id) => {
      const root = cy.getElementById(id)
      if (root.empty()) return
      // Impact arrows encode deletion direction, not expansion ownership. Use
      // expandedFrom to remove this node's exclusive drill-down branch while
      // retaining nodes that are also connected by an initial or shared edge.
      const removeExclusiveChildren = (parentID: string): void => {
        const ownedEdges = cy.edges().filter((edge: cytoscape.EdgeSingular) => edge.data('expandedFrom') === parentID)
        const childIDs = new Set<string>()
        ownedEdges.forEach((edge: cytoscape.EdgeSingular) => {
          const source = edge.source().id()
          const target = edge.target().id()
          childIDs.add(source === parentID ? target : source)
        })
        cy.remove(ownedEdges)
        childIDs.forEach(childID => {
          if (childID === parentID) return
          const child = cy.getElementById(childID)
          if (child.empty() || child.data('tier') === 'cluster') return
          const shared = child.connectedEdges().some(edge => {
            const owner = edge.data('expandedFrom') as string | undefined
            return !owner || owner !== childID
          })
          if (shared) return
          removeExclusiveChildren(childID)
          cy.remove(child)
        })
      }
      removeExclusiveChildren(id)
      root.data('expanded', 'false')
    },
    relayout: (layout) => {
      cy.layout((layout ?? { name: 'breadthfirst', directed: true, spacingFactor: 1.0, padding: 20 }) as unknown as cytoscape.LayoutOptions).run()
    },
    panBy: (dx, dy) => cy.panBy({ x: dx, y: dy }),
    zoomBy: (factor) => cy.zoom({ level: cy.zoom() * factor, renderedPosition: { x: cy.width() / 2, y: cy.height() / 2 } }),
    fit: () => {
      cy.resize()
      cy.fit(undefined, 24)
    },
    restyle: (style) => { cy.style(style).update() },
    nodeCount: () => cy.nodes().length,
  }
}

function shortName(o: ObjectResult): string {
  const m = o.object?.metadata ?? {}
  return `${m.namespace ? m.namespace + '/' : ''}${m.name ?? '?'}`
}

// edgeOf strips the "{tenant}/" prefix from an engaged cluster key — mirror
// of the same helper in element.ts.
function edgeOf(cluster?: string): string {
  if (!cluster) return '?'
  const i = cluster.lastIndexOf('/')
  return i === -1 ? cluster : cluster.slice(i + 1)
}
