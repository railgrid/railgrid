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
      elements.push({ data: { id: `${anchorId}|${rel}|${id}`, source: anchorId, target: id, rel } })
    })
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
): Promise<GraphHandle> {
  const cytoscape = await loadCytoscape(libUrl)
  const cy = cytoscape({
    container,
    elements,
    style,
    layout: {
      name: 'concentric',
      concentric: (node: cytoscape.NodeSingular) => (node.data('anchor') === 'true' ? 2 : 1),
      levelWidth: () => 1,
      minNodeSpacing: 34,
      padding: 16,
    },
    wheelSensitivity: 0.2,
    minZoom: 0.2,
    maxZoom: 3,
  })
  cy.on('tap', 'node', (evt: cytoscape.EventObject) => {
    const n = evt.target
    if (n.data('anchor') !== 'true') onNodeTap(n.id())
  })
  return { destroy: () => cy.destroy() }
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
