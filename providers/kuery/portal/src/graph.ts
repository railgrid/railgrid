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
import type { ObjectResult } from './element'

// The UMD bundle assigns this global.
declare global {
  interface Window {
    cytoscape?: typeof cytoscape
  }
}

// Relation type → edge color. These double as the legend swatches in
// element.ts, so the graph and its legend can never drift. Keys match the
// IMPACT_RELATIONS set the impact query requests.
export const RELATION_COLORS: Record<string, string> = {
  owners: '#e0b34f',
  'descendants+': '#4f9be0',
  references: '#9b6de0',
  selects: '#4fe0a8',
  'selected-by': '#e07a4f',
  'linked+': '#e0519b',
  grouped: '#8a93a8',
  namespace: '#5fae7a',
  namespaced: '#5fae7a',
}

// Short legend labels (the impact list uses longer titles).
export const RELATION_LABELS: Record<string, string> = {
  owners: 'owners',
  'descendants+': 'descendants',
  references: 'references',
  selects: 'selects',
  'selected-by': 'selected-by',
  'linked+': 'linked',
  grouped: 'grouped',
  namespace: 'namespace',
  namespaced: 'contains',
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
export const RELATION_DIR: Record<string, RelDir> = {
  owners: 'up',
  references: 'up',
  selects: 'up',
  namespace: 'up',
  descendants: 'down',
  'descendants+': 'down',
  'selected-by': 'down',
  namespaced: 'down',
  linked: 'lateral',
  'linked+': 'lateral',
  grouped: 'lateral',
}

// orientEdge returns [source, target] for an edge between the anchor and a
// related node, per the relation's impact direction (see RELATION_DIR).
function orientEdge(anchorId: string, relatedId: string, rel: string): [string, string] {
  return (RELATION_DIR[rel] ?? 'down') === 'up' ? [relatedId, anchorId] : [anchorId, relatedId]
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

  const anchorId = anchor.id || 'anchor'
  pushNode(elements, nodeIndex, seen, anchorId, anchor, true)

  const rels = anchor.relations ?? {}
  for (const [rel, items] of Object.entries(rels)) {
    ;(items ?? []).forEach((it, i) => {
      const id = it.id || `${rel}:${i}`
      pushNode(elements, nodeIndex, seen, id, it, false)
      const [source, target] = orientEdge(anchorId, id, rel)
      elements.push({ data: { id: `${source}>${target}`, source, target, rel } })
    })
  }
  return { elements, nodeIndex }
}

// buildTopologyElements turns a clusters-rooted query result (each cluster
// with its `members` relation) into a fleet tree: Edge → Namespace → object,
// with cluster-scoped objects hanging straight off the Edge. Structural edges
// all use rel "namespace" so they share the containment color.
//
// A namespace's tier node IS the real `Namespace` object when one was synced
// (so it carries children AND is clickable → its `namespaced` impact), rather
// than rendering the Namespace both as a synthetic group and a childless leaf.
// Falls back to a synthetic tier when the Namespace object isn't in the set.
export function buildTopologyElements(
  clusters: ObjectResult[],
  opts?: { kind?: string; namespace?: string },
): BuildResult {
  const elements: cytoscape.ElementDefinition[] = []
  const nodeIndex: Record<string, ObjectResult> = {}
  const nodes = new Set<string>()
  const edges = new Set<string>()
  const wantKind = opts?.kind || ''
  const wantNs = opts?.namespace || ''

  const addNode = (id: string, data: Record<string, unknown>) => {
    if (nodes.has(id)) return
    nodes.add(id)
    elements.push({ data: { id, ...data } })
  }
  const addEdge = (source: string, target: string) => {
    const id = `${source}>${target}`
    if (edges.has(id)) return
    edges.add(id)
    elements.push({ data: { id, source, target, rel: 'namespace' } })
  }

  for (const c of clusters) {
    const cname = c.cluster || c.object?.metadata?.name || 'cluster'
    const cid = `cluster:${cname}`
    addNode(cid, { label: edgeOf(cname), tier: 'cluster', anchor: 'true', kind: 'Cluster', name: edgeOf(cname) })

    const members = c.relations?.members ?? []

    // Index the real Namespace objects so a tier can adopt one as its node.
    const nsObjByName = new Map<string, ObjectResult>()
    for (const mem of members) {
      if (mem.object?.kind === 'Namespace') nsObjByName.set(mem.object.metadata?.name || '', mem)
    }
    // ensureNs returns the tier node id for a namespace, creating it (and the
    // Edge→Namespace edge) on first use. Uses the real Namespace object's id
    // when available so the tier is the object itself.
    const ensureNs = (nsName: string): string => {
      const real = nsObjByName.get(nsName)
      const nid = real?.id || `ns:${cname}/${nsName}`
      if (!nodes.has(nid)) {
        addNode(nid, { label: nsName, tier: 'namespace', kind: 'Namespace', name: nsName })
        if (real) nodeIndex[nid] = real
        addEdge(cid, nid)
      }
      return nid
    }

    // Show every namespace as a tier — even empty ones — unless filtering to a
    // different kind.
    if (!wantKind || wantKind === 'Namespace') {
      for (const nsName of nsObjByName.keys()) {
        if (!nsName) continue
        if (wantNs && nsName !== wantNs) continue
        ensureNs(nsName)
      }
    }

    for (const mem of members) {
      const o = mem.object ?? {}
      const kind = o.kind || '?'
      if (kind === 'Namespace') continue // already represented as a tier node
      const name = o.metadata?.name || '?'
      const ns = o.metadata?.namespace || ''
      // Client-side facet filters. Namespace "" (cluster-scoped) is excluded
      // when a specific namespace is selected.
      if (wantKind && kind !== wantKind) continue
      if (wantNs && ns !== wantNs) continue
      const oid = mem.id || `${cname}/${kind}/${ns}/${name}`
      const parent = ns ? ensureNs(ns) : cid
      addNode(oid, { label: `${kind}\n${name}`, tier: 'object', kind, name, edge: edgeOf(mem.cluster) })
      nodeIndex[oid] = mem
      addEdge(parent, oid)
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
  const text = v('--color-text-primary', '#e6e6f0')
  const muted = v('--color-text-muted', '#9aa0ad')
  const accent = v('--color-accent', '#6d4fe0')

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
      },
    },
  ]
  for (const [rel, color] of Object.entries(RELATION_COLORS)) {
    style.push({ selector: `edge[rel = "${rel}"]`, style: { 'line-color': color, 'target-arrow-color': color } })
  }
  return style
}

export interface GraphHandle {
  destroy(): void
  // add merges new nodes/edges into the live graph, skipping ids already
  // present (so an object reached from two parents becomes one node with two
  // edges — the real dependency net). Returns the elements actually added.
  add(elements: cytoscape.ElementDefinition[]): cytoscape.ElementDefinition[]
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
  nodeCount(): number
}

// relationElements builds the child nodes/edges for one already-placed node
// (anchorId) from an impact result's relations. It does NOT emit the anchor
// itself. Child node ids are the objects' stable kuery ids, so re-expanding or
// reaching the same object from elsewhere dedupes to a single node. Edge ids
// are per (parent, rel, child) so parallel relations stay distinct.
export function relationElements(anchorId: string, anchor: ObjectResult): BuildResult {
  const elements: cytoscape.ElementDefinition[] = []
  const nodeIndex: Record<string, ObjectResult> = {}
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
      // (related → anchor), downstream out of it. Endpoint-based edge id so a
      // pair reached twice (e.g. the namespace already linked in the base tree)
      // dedupes to one edge.
      const [source, target] = orientEdge(anchorId, id, rel)
      elements.push({ data: { id: `${source}>${target}`, source, target, rel } })
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
    wheelSensitivity: 0.2,
    // Allow zooming far out so a fully-expanded net still fits on screen.
    minZoom: 0.02,
    maxZoom: 3,
  })
  // Every node is tappable; the caller decides what (if anything) to do — for
  // the explorer that's expand/collapse, for the impact view it re-anchors.
  cy.on('tap', 'node', (evt: cytoscape.EventObject) => onNodeTap(evt.target.id()))

  return {
    destroy: () => cy.destroy(),
    hasNode: (id) => cy.getElementById(id).nonempty(),
    isExpanded: (id) => cy.getElementById(id).data('expanded') === 'true',
    markExpanded: (id, expanded) => {
      const n = cy.getElementById(id)
      if (n.nonempty()) n.data('expanded', expanded ? 'true' : 'false')
    },
    add: (els) => {
      const fresh = els.filter((e) => {
        const id = e.data?.id as string | undefined
        return !!id && cy.getElementById(id).empty()
      })
      if (fresh.length) cy.add(fresh)
      return fresh
    },
    collapseFrom: (id) => {
      const root = cy.getElementById(id)
      if (root.empty()) return
      // Cut this node's outgoing edges, then cascade-remove any node left with
      // no incoming edge (its exclusive subtree). Shared nodes keep an edge
      // from elsewhere and survive; cluster roots are never removed.
      cy.remove(root.connectedEdges().filter((e: cytoscape.EdgeSingular) => e.source().id() === id))
      let changed = true
      while (changed) {
        changed = false
        cy.nodes().forEach((n: cytoscape.NodeSingular) => {
          if (n.id() === id || n.data('tier') === 'cluster') return
          if (n.incomers('edge').empty()) {
            cy.remove(n)
            changed = true
          }
        })
      }
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
