// KueryElement — the kuery provider's portal UI: a fleet-wide object
// inventory over every edge connected to the workspace, with an impact
// drill-down (declared blast radius of one object). Backed by the
// provider's tenant-scoped /api/query + /api/edges (proxied by the hub
// at /services/providers/kuery/*, which injects X-Kedge-Tenant).
//
// Plain custom element in light DOM (the portal's CSS variables cascade
// in); see main.ts for registration and style.css for the rules.

import {
  buildElements,
  buildTopologyElements,
  relationElements,
  themeStyle,
  mountGraph,
  RELATION_COLORS,
  RELATION_LABELS,
  RELATION_DIR,
  type GraphHandle,
} from './graph'
import { loadCodeMirror, collectSchemaWords, createEditor, EXAMPLES, type EditorHandle } from './playground'

export interface KedgeContext {
  token?: string | null
  user?: { email?: string; sub?: string } | null
  tenant?: string | null
  // Sidebar-selected org/workspace UUIDs. Forwarded as X-Kedge-Org /
  // X-Kedge-Workspace so the hub backend proxy can resolve and inject
  // X-Kedge-Tenant; without them the resolver falls back to the user's
  // personal org (often unset in dev) and the backend 401s.
  orgUUID?: string | null
  workspaceUUID?: string | null
  theme?: 'light' | 'dark' | 'system'
  basePath?: string
}

// Minimal mirror of kuery's ObjectResult — only what the UI renders.
// Exported so graph.ts can type its node index against the same shape.
export interface ObjectResult {
  id?: string
  cluster?: string
  object?: {
    kind?: string
    apiVersion?: string
    metadata?: { name?: string; namespace?: string; creationTimestamp?: string }
  }
  relations?: Record<string, ObjectResult[]>
}

interface QueryStatus {
  objects?: ObjectResult[]
  incomplete?: boolean
  warnings?: string[]
}

// The relation set the impact view expands — keep in lockstep with
// mcpserver/tools.go impactRelations.
const IMPACT_RELATIONS = ['descendants+', 'references', 'selects', 'selected-by', 'owners', 'linked+', 'grouped', 'namespace', 'namespaced']

const RELATION_TITLES: Record<string, string> = {
  'descendants+': 'Descendants (transitive)',
  references: 'References (spec fields)',
  selects: 'Selects (label selectors)',
  'selected-by': 'Selected by',
  owners: 'Owners',
  'linked+': 'Linked (cross-edge, transitive)',
  grouped: 'Grouped (cross-edge)',
  namespace: 'Namespace',
  namespaced: 'Contains (namespace members)',
}

export class KueryElement extends HTMLElement {
  private _ctx: KedgeContext | null = null
  private _booted = false

  private _edges: string[] = []
  private _rows: ObjectResult[] = []
  private _incomplete = false
  private _queryError = ''
  private _loading = false

  // Top-level view. Topology (edge-centric fleet tree) is the landing view;
  // Inventory (flat table) is the alternate. Each loads its data lazily.
  private _view: 'topology' | 'inventory' | 'playground' = 'topology'
  private _inventoryLoaded = false
  private _topology: ObjectResult[] = []
  private _topologyError = ''
  private _topologyLoaded = false

  // Topology graph controls — all client-side (no refetch): layout + facet
  // filters re-render from the cached _topology. Edge filter does refetch.
  private _topoLayout: 'breadthfirst' | 'concentric' | 'circle' | 'cose' = 'breadthfirst'
  private _topoKind = ''
  private _topoNamespace = ''
  private _topoFull = false
  private _expandingAll = false
  private _boundKeyHandler = (ev: KeyboardEvent) => this._onKeyDown(ev)
  private _boundFsHandler = () => this._onFullscreenChange()

  // Playground state: the live editor, last result/error, and the schema
  // vocabulary that drives autocomplete (fetched once).
  private _pgEditor: EditorHandle | null = null
  private _pgResult = ''
  private _pgError = ''
  private _pgRunning = false
  private _pgWords: string[] = []
  private _pgDoc = JSON.stringify(EXAMPLES[0].spec, null, 2)

  // Impact drill-down state. null = inventory view.
  private _impactOf: ObjectResult | null = null
  private _impact: ObjectResult | null = null
  private _impactError = ''
  private _impactView: 'graph' | 'list' = 'graph'

  // Live Cytoscape instance + a generation counter. _render() rewrites
  // innerHTML wholesale (destroying the canvas), so every render tears the
  // graph down first and a fresh async mount tags itself with _graphGen to
  // detect — and discard — itself if a newer render superseded it.
  private _cy: GraphHandle | null = null
  private _graphGen = 0

  // Explorer state for the topology graph: node id → its ObjectResult (so a tap
  // can query that object's relations) and which taps are in flight.
  private _graphObjects = new Map<string, ObjectResult>()
  private _expanding = new Set<string>()

  // Filter state survives re-renders.
  private _fEdge = ''
  private _fKind = ''
  private _fNamespace = ''
  private _fName = ''

  set kedgeContext(v: KedgeContext | null) {
    this._ctx = v
    this._render()
    this._boot()
  }
  get kedgeContext(): KedgeContext | null {
    return this._ctx
  }

  connectedCallback(): void {
    window.addEventListener('keydown', this._boundKeyHandler)
    document.addEventListener('fullscreenchange', this._boundFsHandler)
    this._render()
    this._boot()
  }

  disconnectedCallback(): void {
    window.removeEventListener('keydown', this._boundKeyHandler)
    document.removeEventListener('fullscreenchange', this._boundFsHandler)
    this._destroyGraph()
    this._pgEditor?.destroy()
    this._pgEditor = null
  }

  // _boot waits for basePath (it can arrive on a later context push), then
  // loads the edge list and the initial (topology) view.
  private _boot(): void {
    if (this._booted || !this._ctx?.basePath) return
    this._booted = true
    void this._loadEdges()
    void this._runTopology()
  }

  private _apiBase(): string {
    return (this._ctx?.basePath || '').replace(/^\/ui\/providers\//, '/services/providers/')
  }

  // _tenantHeaders carries the identity the hub backend proxy needs to
  // inject X-Kedge-Tenant: the bearer token (to identify the user) plus
  // the sidebar-selected org/workspace (to pick the tenant). Mirrors what
  // the console's own /api/orgs/* requests send.
  private _tenantHeaders(): Record<string, string> {
    const h: Record<string, string> = {}
    if (this._ctx?.token) h['Authorization'] = `Bearer ${this._ctx.token}`
    if (this._ctx?.orgUUID) h['X-Kedge-Org'] = this._ctx.orgUUID
    if (this._ctx?.workspaceUUID) h['X-Kedge-Workspace'] = this._ctx.workspaceUUID
    return h
  }

  private async _fetchJSON(path: string, init?: RequestInit): Promise<unknown> {
    const res = await fetch(this._apiBase() + path, {
      credentials: 'same-origin',
      ...init,
      headers: { ...this._tenantHeaders(), ...(init?.headers as Record<string, string> | undefined) },
    })
    const body = await res.text()
    if (!res.ok) throw new Error(`${res.status} ${body.slice(0, 300)}`)
    return body ? JSON.parse(body) : {}
  }

  private async _loadEdges(): Promise<void> {
    try {
      const out = (await this._fetchJSON('/api/edges')) as { edges?: string[] }
      this._edges = out.edges ?? []
    } catch {
      this._edges = []
    }
    this._render()
  }

  private async _runQuery(): Promise<void> {
    this._loading = true
    this._queryError = ''
    this._render()

    const filter: Record<string, unknown> = {}
    if (this._fKind) filter.groupKind = { kind: this._fKind }
    if (this._fNamespace) filter.namespace = this._fNamespace
    if (this._fName) filter.name = this._fName

    const spec: Record<string, unknown> = {
      limit: 100,
      objects: { id: true, cluster: true },
    }
    if (this._fEdge) spec.cluster = { name: this._fEdge }
    if (Object.keys(filter).length > 0) spec.filter = { objects: [filter] }

    try {
      const status = (await this._fetchJSON('/api/query', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(spec),
      })) as QueryStatus
      this._rows = status.objects ?? []
      this._incomplete = !!status.incomplete
    } catch (e) {
      this._rows = []
      this._queryError = e instanceof Error ? e.message : String(e)
    }
    this._loading = false
    this._inventoryLoaded = true
    this._render()
  }

  // _runTopology fetches the fleet tree: a clusters-rooted query whose
  // `members` relation expands each engaged cluster to its objects. The graph
  // (buildTopologyElements) groups members under synthetic namespace tiers, so
  // the result reads Edge → Namespace → object. Scoped to one edge if filtered.
  private async _runTopology(): Promise<void> {
    this._loading = true
    this._topologyError = ''
    this._render()

    const memberObjects = {
      id: true,
      cluster: true,
      object: { kind: true, apiVersion: true, metadata: { name: true, namespace: true } },
    }
    const spec: Record<string, unknown> = {
      root: 'clusters',
      objects: {
        id: true,
        cluster: true,
        relations: { members: { limit: 1000, objects: memberObjects } },
      },
    }
    if (this._fEdge) spec.cluster = { name: this._fEdge }

    try {
      const status = (await this._fetchJSON('/api/query', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(spec),
      })) as QueryStatus
      this._topology = status.objects ?? []
      this._incomplete = !!status.incomplete
    } catch (e) {
      this._topology = []
      this._topologyError = e instanceof Error ? e.message : String(e)
    }
    this._loading = false
    this._topologyLoaded = true
    this._render()
  }

  // _impactRelationsObj is the relations projection for the IMPACT_RELATIONS
  // set. `namespaced` (a Namespace's members) can be large, so cap it; the
  // others are naturally bounded by the object's coupling.
  private _impactRelationsObj(): Record<string, unknown> {
    const relations: Record<string, unknown> = {}
    for (const rel of IMPACT_RELATIONS) relations[rel] = rel === 'namespaced' ? { limit: 200 } : {}
    return relations
  }

  // _impactSpecFor builds the QuerySpec that expands one object's declared
  // coupling (the IMPACT_RELATIONS set). Shared by the impact view and by
  // in-graph expansion so both ask for exactly the same relations.
  private _impactSpecFor(row: ObjectResult): Record<string, unknown> {
    const relations = this._impactRelationsObj()

    const o = row.object ?? {}
    const meta = o.metadata ?? {}
    const group = (o.apiVersion || '').includes('/') ? (o.apiVersion as string).split('/')[0] : ''
    const filter: Record<string, unknown> = { name: meta.name }
    if (o.kind) filter.groupKind = { apiGroup: group, kind: o.kind }
    if (meta.namespace) filter.namespace = meta.namespace

    const spec: Record<string, unknown> = {
      maxDepth: 5,
      filter: { objects: [filter] },
      objects: { id: true, cluster: true, relations },
    }
    // row.cluster is "{tenant}/{edge}" — the API re-pins the prefix, so
    // passing it back verbatim is fine and keeps the anchor unambiguous.
    if (row.cluster) spec.cluster = { name: row.cluster }
    return spec
  }

  // _queryRelations fetches one object's coupling and returns the single result
  // (with its relations populated), or null. Used by in-graph expansion.
  private async _queryRelations(row: ObjectResult): Promise<ObjectResult | null> {
    try {
      const status = (await this._fetchJSON('/api/query', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(this._impactSpecFor(row)),
      })) as QueryStatus
      return status.objects?.[0] ?? null
    } catch {
      return null
    }
  }

  private async _runImpact(row: ObjectResult): Promise<void> {
    this._impactOf = row
    this._impact = null
    this._impactError = ''
    this._render()

    try {
      const status = (await this._fetchJSON('/api/query', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(this._impactSpecFor(row)),
      })) as QueryStatus
      this._impact = status.objects?.[0] ?? null
      if (!this._impact) this._impactError = 'object not found (sync may be catching up)'
    } catch (e) {
      this._impactError = e instanceof Error ? e.message : String(e)
    }
    this._render()
  }

  // _expandInGraph grows the live topology graph in place: tapping a node
  // queries its coupling and grafts the related objects on (deduping shared
  // ones), so you can walk the dependency net without losing what you've
  // already revealed. Tapping an already-expanded node collapses its subtree.
  // _expandOne queries one node's coupling and grafts the related objects onto
  // the live graph (no relayout — the caller batches that). Returns how many
  // new nodes were added. Newly added objects join _graphObjects so they too
  // become expandable.
  private async _expandOne(id: string, handle: GraphHandle): Promise<number> {
    const obj = this._graphObjects.get(id)
    if (!obj || handle.isExpanded(id) || this._expanding.has(id)) return 0
    this._expanding.add(id)
    handle.markExpanded(id, true) // optimistic; reflects intent while the query runs
    try {
      const res = await this._queryRelations(obj)
      if (this._cy !== handle) return 0 // a re-render replaced the graph mid-flight
      if (!res) {
        handle.markExpanded(id, false)
        return 0
      }
      const { elements, nodeIndex } = relationElements(id, res)
      const added = handle.add(elements)
      for (const [nid, o] of Object.entries(nodeIndex)) {
        if (!this._graphObjects.has(nid)) this._graphObjects.set(nid, o)
      }
      return added.length
    } finally {
      this._expanding.delete(id)
    }
  }

  private async _expandInGraph(id: string): Promise<void> {
    const handle = this._cy
    if (!handle || !this._graphObjects.has(id)) return
    if (handle.isExpanded(id)) {
      handle.collapseFrom(id) // tap an expanded node to collapse its subtree
      return
    }
    const added = await this._expandOne(id, handle)
    if (added && this._cy === handle) handle.relayout(this._topoLayoutConfig())
  }

  // _expandBatch expands a whole frontier of nodes in ONE query: it filters by
  // all their ids at once (filter.objects[] is OR-ed) and asks for each one's
  // relations, instead of a request per node. Grafts every returned object's
  // relations, marks the whole batch expanded (so empties aren't re-queried),
  // and registers new nodes for further rounds. Returns nodes added.
  private async _expandBatch(ids: string[], handle: GraphHandle): Promise<number> {
    if (ids.length === 0) return 0
    const spec: Record<string, unknown> = {
      maxDepth: 5,
      limit: Math.min(ids.length, 10000),
      filter: { objects: ids.map((id) => ({ id })) },
      objects: { id: true, cluster: true, relations: this._impactRelationsObj() },
    }
    let status: QueryStatus
    try {
      status = (await this._fetchJSON('/api/query', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(spec),
      })) as QueryStatus
    } catch {
      return 0
    }
    if (this._cy !== handle) return 0

    let added = 0
    for (const res of status.objects ?? []) {
      const anchorId = res.id
      if (!anchorId) continue
      const { elements, nodeIndex } = relationElements(anchorId, res)
      added += handle.add(elements).length
      for (const [nid, o] of Object.entries(nodeIndex)) {
        if (!this._graphObjects.has(nid)) this._graphObjects.set(nid, o)
      }
    }
    // Mark the entire requested frontier expanded — including ids that returned
    // nothing — so the next round doesn't re-query them.
    for (const id of ids) handle.markExpanded(id, true)
    return added
  }

  // _expandAll walks the whole reachable net by frontier: each round expands
  // every currently-unexpanded node in a few batched queries (chunked so a
  // single request stays bounded), until nothing new appears. Bounded by a node
  // cap and round limit so a large fleet can't run away.
  private async _expandAll(): Promise<void> {
    const handle = this._cy
    if (!handle || this._expandingAll) return
    this._expandingAll = true
    const btn = this.querySelector('#t-expand-all')
    if (btn) btn.textContent = 'Expanding…'
    const MAX_NODES = 4000
    const CHUNK = 300
    try {
      for (let round = 0; round < 30; round++) {
        if (this._cy !== handle || handle.nodeCount() >= MAX_NODES) break
        const todo = [...this._graphObjects.keys()].filter((id) => handle.hasNode(id) && !handle.isExpanded(id))
        if (todo.length === 0) break
        const chunks: string[][] = []
        for (let i = 0; i < todo.length; i += CHUNK) chunks.push(todo.slice(i, i + CHUNK))
        const counts = await Promise.all(chunks.map((c) => this._expandBatch(c, handle)))
        if (this._cy !== handle) break
        handle.relayout(this._topoLayoutConfig())
        if (counts.every((n) => n === 0)) break // frontier produced nothing new
      }
    } finally {
      this._expandingAll = false
      const b = this.querySelector('#t-expand-all')
      if (b) b.textContent = 'Expand all'
    }
  }

  // ── rendering ────────────────────────────────────────────────────────

  private _render(): void {
    // Tear down any live graph/editor before innerHTML replaces their host.
    this._destroyGraph()
    if (this._pgEditor) {
      this._pgDoc = this._pgEditor.getValue() // keep the user's draft across renders
      this._pgEditor.destroy()
      this._pgEditor = null
    }

    if (this._impactOf) {
      this.innerHTML = this._renderImpact()
      this.querySelector('#impact-back')?.addEventListener('click', () => {
        this._impactOf = null
        this._impact = null
        this._render()
      })
      this._bindImpactToggle()
      if (this._impactView === 'graph' && this._impact && !this._impactError) void this._mountGraph()
      return
    }
    if (this._view === 'topology') {
      this.innerHTML = this._renderTopology()
      this._bindTopology()
      if (this._topology.length && !this._topologyError && !this._loading) void this._mountTopologyGraph()
      return
    }
    if (this._view === 'playground') {
      this.innerHTML = this._renderPlayground()
      this._bindPlayground()
      void this._mountEditor()
      return
    }
    this.innerHTML = this._renderInventory()
    this._bindInventory()
  }

  // _viewToggle is the top-level Topology/Inventory/Playground switch.
  private _viewToggle(): string {
    const btn = (v: string, label: string) =>
      `<button class="seg-btn${this._view === v ? ' on' : ''}" data-topview="${v}">${label}</button>`
    return `<div class="seg">${btn('topology', 'Topology')}${btn('inventory', 'Inventory')}${btn('playground', 'Playground')}</div>`
  }

  private _bindViewToggle(): void {
    this.querySelectorAll('[data-topview]').forEach((b) =>
      b.addEventListener('click', () => {
        const v = (b as HTMLElement).dataset.topview as 'topology' | 'inventory' | 'playground' | undefined
        if (!v || v === this._view) return
        this._view = v
        // Load the target view's data on first visit; otherwise just re-render.
        if (v === 'topology' && !this._topologyLoaded) void this._runTopology()
        else if (v === 'inventory' && !this._inventoryLoaded) void this._runQuery()
        else this._render()
      }),
    )
  }

  private _destroyGraph(): void {
    this._graphGen++ // invalidate any in-flight async mount
    if (this._cy) {
      this._cy.destroy()
      this._cy = null
    }
  }

  // _mountGraph draws the current impact result into #kuery-graph. mountGraph
  // lazy-loads Cytoscape, so this is async; _graphGen lets a mount that loses
  // the race to a newer render discard itself.
  private async _mountGraph(): Promise<void> {
    const container = this.querySelector('#kuery-graph') as HTMLElement | null
    if (!container || !this._impact) return
    const gen = this._graphGen
    try {
      const { elements, nodeIndex } = buildElements(this._impact)
      // Vendored UMD bundle, served from this provider's own dist/ root
      // (basePath is /ui/providers/kuery). The hub hands basePath WITHOUT a
      // trailing slash, so normalize to exactly one before appending — a naive
      // concat yields /ui/providers/kuerycytoscape.min.js (404). Injected on
      // demand by graph.ts.
      const libUrl = `${(this._ctx?.basePath || '').replace(/\/?$/, '/')}cytoscape.min.js`
      const handle = await mountGraph(
        container,
        elements,
        themeStyle(this),
        (id) => {
          if (id === this._impact?.id) return // tapping the anchor is a no-op
          const obj = nodeIndex[id]
          if (obj) void this._runImpact(obj)
        },
        libUrl,
      )
      if (gen !== this._graphGen) {
        handle.destroy() // superseded while Cytoscape was loading
        return
      }
      this._cy = handle
    } catch (e) {
      container.innerHTML = `<p class="error">graph failed to load: ${esc(e instanceof Error ? e.message : String(e))}</p>`
    }
  }

  private _bindImpactToggle(): void {
    this.querySelectorAll('.seg-btn').forEach((b) =>
      b.addEventListener('click', () => {
        const v = (b as HTMLElement).dataset.view as 'graph' | 'list' | undefined
        if (v && v !== this._impactView) {
          this._impactView = v
          this._render()
        }
      }),
    )
  }

  // _mountTopologyGraph draws the fleet tree into #kuery-graph using a
  // top-down breadthfirst layout (Edge at the root). Same lazy-load + _graphGen
  // race guard as _mountGraph; tapping an object node drills into its impact.
  private async _mountTopologyGraph(): Promise<void> {
    const container = this.querySelector('#kuery-graph') as HTMLElement | null
    if (!container) return
    const gen = this._graphGen
    try {
      const { elements, nodeIndex } = buildTopologyElements(this._topology, {
        kind: this._topoKind,
        namespace: this._topoNamespace,
      })
      // Seed the explorer map from the base tree so any node can be expanded.
      this._graphObjects = new Map(Object.entries(nodeIndex))
      this._expanding = new Set()
      const libUrl = `${(this._ctx?.basePath || '').replace(/\/?$/, '/')}cytoscape.min.js`
      const handle = await mountGraph(
        container,
        elements,
        themeStyle(this),
        (id) => void this._expandInGraph(id),
        libUrl,
        this._topoLayoutConfig(),
      )
      if (gen !== this._graphGen) {
        handle.destroy()
        return
      }
      this._cy = handle
    } catch (e) {
      container.innerHTML = `<p class="error">graph failed to load: ${esc(e instanceof Error ? e.message : String(e))}</p>`
    }
  }

  // _topoLayoutConfig maps the selected layout to a Cytoscape layout. cose is
  // force-directed (draggable, physics-settled); concentric is radial by tier;
  // circle rings everything; breadthfirst is the top-down tree.
  private _topoLayoutConfig(): Record<string, unknown> {
    switch (this._topoLayout) {
      case 'concentric':
        return {
          name: 'concentric',
          concentric: (n: { data: (k: string) => unknown }) =>
            n.data('tier') === 'cluster' ? 3 : n.data('tier') === 'namespace' ? 2 : 1,
          levelWidth: () => 1,
          minNodeSpacing: 30,
          padding: 20,
        }
      case 'circle':
        return { name: 'circle', padding: 20 }
      case 'cose':
        return { name: 'cose', idealEdgeLength: 70, nodeRepulsion: 9000, padding: 20, animate: false }
      default:
        return { name: 'breadthfirst', directed: true, spacingFactor: 1.0, padding: 20 }
    }
  }

  // _topologyFacets collects the kinds and namespaces actually present in the
  // current fleet so the filter dropdowns offer real choices (pre-selects).
  private _topologyFacets(): { kinds: string[]; namespaces: string[] } {
    const kinds = new Set<string>()
    const namespaces = new Set<string>()
    for (const c of this._topology) {
      for (const m of c.relations?.members ?? []) {
        const o = m.object ?? {}
        if (o.kind) kinds.add(o.kind)
        const ns = o.metadata?.namespace
        if (ns) namespaces.add(ns)
      }
    }
    return { kinds: [...kinds].sort(), namespaces: [...namespaces].sort() }
  }

  private _renderTopology(): string {
    const opt = (value: string, label: string, sel: string) =>
      `<option value="${esc(value)}"${value === sel ? ' selected' : ''}>${esc(label)}</option>`
    const edgeOptions = [opt('', 'all edges', this._fEdge)]
      .concat(this._edges.map((e) => opt(e, e, this._fEdge)))
      .join('')

    const layouts: Array<[typeof this._topoLayout, string]> = [
      ['breadthfirst', 'Tree'],
      ['concentric', 'Radial'],
      ['circle', 'Circle'],
      ['cose', 'Force'],
    ]
    const layoutOptions = layouts.map(([v, l]) => opt(v, l, this._topoLayout)).join('')

    const facets = this._topologyFacets()
    const kindOptions = [opt('', 'all kinds', this._topoKind)]
      .concat(facets.kinds.map((k) => opt(k, k, this._topoKind)))
      .join('')
    const nsOptions = [opt('', 'all namespaces', this._topoNamespace)]
      .concat(facets.namespaces.map((n) => opt(n, n, this._topoNamespace)))
      .join('')

    let body: string
    if (this._loading) {
      body = `<p class="muted">building fleet topology…</p>`
    } else if (this._topologyError) {
      body = `<p class="error">${esc(this._topologyError)}</p>`
    } else if (this._topology.length === 0) {
      body = `<p class="muted">no clusters engaged — connect a kubernetes edge to see its tree</p>`
    } else {
      body = `<div id="kuery-graph" class="kuery-graph"></div>`
    }

    return `
      <div class="panel${this._topoFull ? ' kuery-full' : ''}">
        <div class="panel-head">
          <h2 class="panel-title">Fleet topology</h2>
          <div class="head-actions">
            ${this._viewToggle()}
            <span class="badge ${this._edges.length ? 'ok' : 'warn'}">${this._edges.length} edge${this._edges.length === 1 ? '' : 's'} engaged</span>
          </div>
        </div>
        <p class="meta">Click a node to expand, again to collapse; <b>Expand all</b> walks the whole net. Arrows show impact flow: <b>A→B</b> means deleting A breaks B — so a Namespace/owner points <i>into</i> its pods, not out. Pan with arrows/WASD, zoom +/−, <b>F</b> full screen.</p>
        <div class="toolbar">
          <select id="t-layout" title="layout">${layoutOptions}</select>
          <select id="f-edge" title="edge">${edgeOptions}</select>
          <select id="t-kind" title="kind">${kindOptions}</select>
          <select id="t-ns" title="namespace">${nsOptions}</select>
          <button id="t-expand-all" type="button">Expand all</button>
          <button id="t-reset" type="button">Reset</button>
          <button id="t-full" type="button">${this._topoFull ? 'Exit full screen' : 'Full screen'}</button>
        </div>
        ${body}
        ${this._incomplete ? '<p class="meta">tree truncated — filter to one edge to see all of it</p>' : ''}
      </div>
    `
  }

  private _bindTopology(): void {
    this._bindViewToggle()
    // Edge change refetches (it scopes the server query); layout/kind/namespace
    // are pure client-side re-renders off the cached _topology.
    this.querySelector('#f-edge')?.addEventListener('change', () => {
      this._fEdge = (this.querySelector('#f-edge') as HTMLSelectElement | null)?.value ?? ''
      void this._runTopology()
    })
    this.querySelector('#t-layout')?.addEventListener('change', () => {
      this._topoLayout = ((this.querySelector('#t-layout') as HTMLSelectElement | null)?.value || 'breadthfirst') as typeof this._topoLayout
      // Relayout the live graph in place so expansions survive a layout switch;
      // only fall back to a full render if the graph isn't mounted yet.
      if (this._cy) this._cy.relayout(this._topoLayoutConfig())
      else this._render()
    })
    this.querySelector('#t-kind')?.addEventListener('change', () => {
      this._topoKind = (this.querySelector('#t-kind') as HTMLSelectElement | null)?.value ?? ''
      this._render()
    })
    this.querySelector('#t-ns')?.addEventListener('change', () => {
      this._topoNamespace = (this.querySelector('#t-ns') as HTMLSelectElement | null)?.value ?? ''
      this._render()
    })
    this.querySelector('#t-expand-all')?.addEventListener('click', () => void this._expandAll())
    this.querySelector('#t-reset')?.addEventListener('click', () => this._render())
    this.querySelector('#t-full')?.addEventListener('click', () => this._toggleFull())
  }

  // _toggleFull takes the graph panel truly full-page via the Fullscreen API,
  // which escapes any transformed/filtered ancestor in the portal shell (a CSS
  // `position: fixed` overlay would otherwise be trapped inside that ancestor
  // and only fill its box). Falls back to the fixed overlay where the API is
  // unavailable. State + refit are driven by the fullscreenchange event.
  private _toggleFull(): void {
    const panel = this.querySelector('.panel') as HTMLElement | null
    if (!panel) return
    // Exit whichever fullscreen mode is currently active first.
    if (document.fullscreenElement) {
      void document.exitFullscreen?.()
      return
    }
    if (this._topoFull) {
      // CSS-overlay fallback is active (no fullscreenElement) — exit it.
      this._toggleFullCSS(panel)
      return
    }
    // Enter: prefer the real Fullscreen API, fall back to the CSS overlay.
    if (panel.requestFullscreen) {
      panel.requestFullscreen().catch(() => this._toggleFullCSS(panel))
      return
    }
    this._toggleFullCSS(panel)
  }

  // _toggleFullCSS is the fallback overlay (fixed inset:0). Works unless a
  // transformed ancestor traps it — hence the Fullscreen API is preferred.
  private _toggleFullCSS(panel: HTMLElement): void {
    this._topoFull = !this._topoFull
    panel.classList.toggle('kuery-full', this._topoFull)
    this._syncFullButton()
    requestAnimationFrame(() => this._cy?.fit())
  }

  private _syncFullButton(): void {
    const full = this.querySelector('#t-full')
    if (full) full.textContent = this._topoFull ? 'Exit full screen' : 'Full screen'
  }

  // _onFullscreenChange keeps state, the button label, the styling class, and
  // the graph's fitted view in sync when entering/exiting API fullscreen
  // (including via the browser's Esc).
  private _onFullscreenChange(): void {
    const panel = this.querySelector('.panel') as HTMLElement | null
    this._topoFull = !!document.fullscreenElement && document.fullscreenElement === panel
    if (panel) panel.classList.toggle('kuery-full', this._topoFull)
    this._syncFullButton()
    requestAnimationFrame(() => this._cy?.fit())
  }

  // _onKeyDown gives the topology graph keyboard navigation: arrows/WASD pan,
  // +/- zoom, F toggles full screen, Esc exits it. Ignored while typing in a
  // form control or when the graph isn't the active view.
  private _onKeyDown(ev: KeyboardEvent): void {
    if (this._view !== 'topology' || this._impactOf || !this._cy) return
    const t = ev.target as HTMLElement | null
    if (t && /^(INPUT|SELECT|TEXTAREA)$/.test(t.tagName)) return
    const PAN = 70
    let handled = true
    switch (ev.key) {
      case 'ArrowUp': case 'w': case 'W': this._cy.panBy(0, PAN); break
      case 'ArrowDown': case 's': case 'S': this._cy.panBy(0, -PAN); break
      case 'ArrowLeft': case 'a': case 'A': this._cy.panBy(PAN, 0); break
      case 'ArrowRight': case 'd': case 'D': this._cy.panBy(-PAN, 0); break
      case '+': case '=': this._cy.zoomBy(1.15); break
      case '-': case '_': this._cy.zoomBy(1 / 1.15); break
      case 'f': case 'F': this._toggleFull(); break
      case 'Escape': if (this._topoFull) this._toggleFull(); else handled = false; break
      default: handled = false
    }
    if (handled) ev.preventDefault()
  }

  // ── playground ────────────────────────────────────────────────────────

  private _renderPlayground(): string {
    const exampleOpts = ['<option value="">examples…</option>']
      .concat(EXAMPLES.map((e, i) => `<option value="${i}">${esc(e.label)}</option>`))
      .join('')
    const results = this._pgError
      ? `<pre class="pg-result error">${esc(this._pgError)}</pre>`
      : `<pre class="pg-result">${esc(this._pgResult || '// results appear here')}</pre>`
    return `
      <div class="panel">
        <div class="panel-head">
          <h2 class="panel-title">Query playground</h2>
          <div class="head-actions">${this._viewToggle()}</div>
        </div>
        <p class="meta">Write a kuery QuerySpec and run it against your fleet. Editor autocompletes from the schema (Ctrl/Cmd-Space). Every query is scoped to your workspace automatically.</p>
        <div class="toolbar">
          <select id="pg-example">${exampleOpts}</select>
          <button id="pg-run">${this._pgRunning ? 'Running…' : 'Run ▸'}</button>
          <button id="pg-docs-toggle" type="button">API &amp; access</button>
        </div>
        <div class="pg-split">
          <div id="kuery-editor" class="pg-editor"></div>
          ${results}
        </div>
        <details id="pg-docs" class="pg-docs">
          <summary>Use this API from outside the portal</summary>
          ${this._renderApiDocs()}
        </details>
      </div>
    `
  }

  private _renderApiDocs(): string {
    const base = this._apiBase()
    const origin = location.origin
    return `
      <p>The same query API is reachable programmatically. The hub authenticates your bearer token and scopes the query to your workspace — you never send a tenant header yourself.</p>
      <pre class="pg-result">curl -sS ${esc(origin)}${esc(base)}/api/query \\
  -H "Authorization: Bearer $KEDGE_TOKEN" \\
  -H "Content-Type: application/json" \\
  -d '{"filter":{"objects":[{"groupKind":{"kind":"Deployment"}}]},"objects":{"cluster":true}}'</pre>
      <ul class="rel-list">
        <li><b>Endpoint</b>: <code>POST ${esc(base)}/api/query</code> (body = QuerySpec, response = QueryStatus)</li>
        <li><b>Schema</b>: <code>GET ${esc(base)}/api/query-schema</code> (JSON Schema for the request body)</li>
        <li><b>Auth</b>: an OIDC bearer token works today. Non-interactive service-account tokens are coming.</li>
        <li><b>Relations</b> carry impact direction: upstream (owners, references, namespace, selects) vs downstream (descendants, selected-by, namespaced, members).</li>
      </ul>`
  }

  private _bindPlayground(): void {
    this._bindViewToggle()
    this.querySelector('#pg-run')?.addEventListener('click', () => void this._runPlayground())
    this.querySelector('#pg-example')?.addEventListener('change', (ev) => {
      const i = Number((ev.target as HTMLSelectElement).value)
      if (Number.isInteger(i) && EXAMPLES[i]) {
        this._pgDoc = JSON.stringify(EXAMPLES[i].spec, null, 2)
        this._pgEditor?.setValue(this._pgDoc)
        this._pgEditor?.refresh()
      }
    })
    this.querySelector('#pg-docs-toggle')?.addEventListener('click', () => {
      const d = this.querySelector('#pg-docs') as HTMLDetailsElement | null
      if (d) d.open = !d.open
    })
  }

  // _mountEditor lazy-loads CodeMirror (and the schema vocabulary, once) and
  // builds the editor into #kuery-editor. Falls back to a plain textarea if the
  // vendored bundle can't load.
  private async _mountEditor(): Promise<void> {
    const host = this.querySelector('#kuery-editor') as HTMLElement | null
    if (!host || this._pgEditor) return
    const baseUI = (this._ctx?.basePath || '').replace(/\/?$/, '/')
    try {
      if (this._pgWords.length === 0) {
        try {
          const schema = await this._fetchJSON('/api/query-schema')
          this._pgWords = collectSchemaWords(schema)
        } catch {
          this._pgWords = []
        }
      }
      const CM = await loadCodeMirror(`${baseUI}codemirror.bundle.js`, `${baseUI}codemirror.bundle.css`)
      if (!this.querySelector('#kuery-editor')) return // re-rendered while loading
      this._pgEditor = createEditor(CM, host, this._pgDoc, this._pgWords)
      this._pgEditor.refresh()
    } catch {
      host.innerHTML = `<textarea id="kuery-editor-fallback" class="pg-fallback">${esc(this._pgDoc)}</textarea>`
    }
  }

  private async _runPlayground(): Promise<void> {
    const raw = this._pgEditor
      ? this._pgEditor.getValue()
      : (this.querySelector('#kuery-editor-fallback') as HTMLTextAreaElement | null)?.value ?? this._pgDoc
    this._pgDoc = raw
    let spec: unknown
    try {
      spec = JSON.parse(raw)
    } catch (e) {
      this._pgError = `invalid JSON: ${e instanceof Error ? e.message : String(e)}`
      this._pgResult = ''
      this._render()
      return
    }
    this._pgRunning = true
    this._pgError = ''
    this._render()
    try {
      const status = await this._fetchJSON('/api/query', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(spec),
      })
      this._pgResult = JSON.stringify(status, null, 2)
    } catch (e) {
      this._pgError = e instanceof Error ? e.message : String(e)
      this._pgResult = ''
    }
    this._pgRunning = false
    this._render()
  }

  private _renderInventory(): string {
    const edgeOptions = ['<option value="">all edges</option>']
      .concat(this._edges.map((e) => `<option value="${esc(e)}"${e === this._fEdge ? ' selected' : ''}>${esc(e)}</option>`))
      .join('')

    let body: string
    if (this._loading) {
      body = `<tr><td colspan="5" class="muted">querying the fleet…</td></tr>`
    } else if (this._queryError) {
      body = `<tr><td colspan="5" class="error">${esc(this._queryError)}</td></tr>`
    } else if (this._rows.length === 0) {
      body = `<tr><td colspan="5" class="muted">no objects match — connect an edge or relax the filters</td></tr>`
    } else {
      body = this._rows.map((r, i) => {
        const o = r.object ?? {}
        const m = o.metadata ?? {}
        return `<tr data-row="${i}" class="row">
          <td><code>${esc(edgeOf(r.cluster))}</code></td>
          <td>${esc(o.kind || '?')}</td>
          <td>${esc(m.namespace || '—')}</td>
          <td class="name">${esc(m.name || '?')}</td>
          <td class="muted">${esc(age(m.creationTimestamp))}</td>
        </tr>`
      }).join('')
    }

    return `
      <div class="panel">
        <div class="panel-head">
          <h2 class="panel-title">Fleet inventory</h2>
          <div class="head-actions">
            ${this._viewToggle()}
            <span class="badge ${this._edges.length ? 'ok' : 'warn'}">${this._edges.length} edge${this._edges.length === 1 ? '' : 's'} engaged</span>
          </div>
        </div>
        <p class="meta">One query across every connected edge. Click a row for its impact (declared blast radius).</p>
        <div class="toolbar">
          <select id="f-edge">${edgeOptions}</select>
          <input id="f-kind" placeholder="kind (e.g. Deployment)" value="${esc(this._fKind)}" />
          <input id="f-ns" placeholder="namespace" value="${esc(this._fNamespace)}" />
          <input id="f-name" placeholder="name (exact)" value="${esc(this._fName)}" />
          <button id="f-go">Search</button>
        </div>
        <table class="objects">
          <thead><tr><th>edge</th><th>kind</th><th>namespace</th><th>name</th><th>age</th></tr></thead>
          <tbody>${body}</tbody>
        </table>
        ${this._incomplete ? '<p class="meta">result truncated — narrow the filters</p>' : ''}
      </div>
    `
  }

  private _bindInventory(): void {
    this._bindViewToggle()
    const read = () => {
      this._fEdge = (this.querySelector('#f-edge') as HTMLSelectElement | null)?.value ?? ''
      this._fKind = (this.querySelector('#f-kind') as HTMLInputElement | null)?.value.trim() ?? ''
      this._fNamespace = (this.querySelector('#f-ns') as HTMLInputElement | null)?.value.trim() ?? ''
      this._fName = (this.querySelector('#f-name') as HTMLInputElement | null)?.value.trim() ?? ''
    }
    this.querySelector('#f-go')?.addEventListener('click', () => {
      read()
      void this._runQuery()
    })
    this.querySelectorAll('input').forEach((el) =>
      el.addEventListener('keydown', (ev) => {
        if ((ev as KeyboardEvent).key === 'Enter') {
          read()
          void this._runQuery()
        }
      }),
    )
    this.querySelectorAll('tr.row').forEach((tr) =>
      tr.addEventListener('click', () => {
        const i = Number((tr as HTMLElement).dataset.row)
        const row = this._rows[i]
        if (row) void this._runImpact(row)
      }),
    )
  }

  private _renderImpact(): string {
    const o = this._impactOf?.object ?? {}
    const m = o.metadata ?? {}
    const title = `${o.kind || '?'} ${m.namespace ? m.namespace + '/' : ''}${m.name || '?'}`

    const hasData = !!this._impact && !this._impactError
    const empty = hasData && !IMPACT_RELATIONS.some((r) => (this._impact!.relations?.[r] ?? []).length > 0)

    let body: string
    if (this._impactError) {
      body = `<p class="error">${esc(this._impactError)}</p>`
    } else if (!this._impact) {
      body = `<p class="muted">expanding declared coupling…</p>`
    } else if (empty) {
      body = '<p class="muted">no declared coupling found — nothing references, selects, or descends from this object (network-level dependencies are not visible to kuery)</p>'
    } else if (this._impactView === 'graph') {
      // The graph mounts into this container after render (see _mountGraph).
      body = `${this._renderLegend()}<div id="kuery-graph" class="kuery-graph"></div>`
    } else {
      body = this._renderImpactList()
    }

    // Only offer the view toggle when there is a graph worth drawing.
    const toggle = hasData && !empty
      ? `<div class="seg">
           <button class="seg-btn${this._impactView === 'graph' ? ' on' : ''}" data-view="graph">Graph</button>
           <button class="seg-btn${this._impactView === 'list' ? ' on' : ''}" data-view="list">List</button>
         </div>`
      : ''

    const hint = this._impactView === 'graph' && hasData && !empty ? ' Click a node to re-center on it.' : ''

    return `
      <div class="panel">
        <div class="panel-head">
          <h2 class="panel-title">Impact: ${esc(title)}</h2>
          <div class="head-actions">
            ${toggle}
            <button id="impact-back">← back</button>
          </div>
        </div>
        <p class="meta">Declared blast radius on <code>${esc(edgeOf(this._impactOf?.cluster))}</code> — owners, descendants, spec references, selector matches, and cross-edge links. Not a network dependency map.${hint}</p>
        ${body}
      </div>
    `
  }

  // _renderLegend maps each relation actually present in the result to its
  // edge color, so the graph's colors are legible. Colors come from graph.ts
  // (RELATION_COLORS) so legend and edges never drift.
  private _renderLegend(): string {
    const rels = this._impact?.relations ?? {}
    const chips = IMPACT_RELATIONS.filter((r) => (rels[r] ?? []).length > 0).map((r) => {
      const color = RELATION_COLORS[r] ?? '#888'
      const label = RELATION_LABELS[r] ?? r
      return `<span class="legend-item"><span class="legend-swatch" style="background:${color}"></span>${esc(label)} <span class="muted">(${(rels[r] ?? []).length})</span></span>`
    })
    return `<div class="legend">${chips.join('')}</div>`
  }

  private _renderImpactList(): string {
    const rels = this._impact?.relations ?? {}
    const present = (r: string) => (rels[r] ?? []).length > 0
    const section = (r: string) => {
      const items = (rels[r] ?? []).map((rr) => {
        const ro = rr.object ?? {}
        const rm = ro.metadata ?? {}
        return `<li><code>${esc(edgeOf(rr.cluster))}</code> ${esc(ro.kind || '?')} <span class="name">${esc(rm.namespace ? rm.namespace + '/' : '')}${esc(rm.name || '?')}</span></li>`
      }).join('')
      return `<h3 class="rel-title">${esc(RELATION_TITLES[r] ?? r)} <span class="muted">(${(rels[r] ?? []).length})</span></h3><ul class="rel-list">${items}</ul>`
    }
    // Two branches: what can break THIS object (upstream) vs what THIS object's
    // deletion breaks (downstream). Lateral relations are peers.
    const group = (dir: string) => IMPACT_RELATIONS.filter((r) => present(r) && (RELATION_DIR[r] ?? 'down') === dir)
    const up = group('up')
    const down = group('down')
    const lat = group('lateral')
    const block = (title: string, rs: string[]) =>
      rs.length ? `<div class="rel-group"><h4 class="rel-group-title">${title}</h4>${rs.map(section).join('')}</div>` : ''
    return (
      block('Impacted by (delete these → breaks this)', up) +
      block('Impacts (delete this → breaks these)', down) +
      block('Associated', lat)
    )
  }
}

// ── helpers ───────────────────────────────────────────────────────────

// edgeOf strips the "{tenant}/" prefix from an engaged cluster key.
function edgeOf(cluster?: string): string {
  if (!cluster) return '?'
  const i = cluster.lastIndexOf('/')
  return i === -1 ? cluster : cluster.slice(i + 1)
}

function age(ts?: string): string {
  if (!ts) return ''
  const ms = Date.now() - new Date(ts).getTime()
  if (!Number.isFinite(ms) || ms < 0) return ''
  const mins = Math.floor(ms / 60000)
  if (mins < 60) return `${mins}m`
  const hours = Math.floor(mins / 60)
  if (hours < 48) return `${hours}h`
  return `${Math.floor(hours / 24)}d`
}

function esc(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}
