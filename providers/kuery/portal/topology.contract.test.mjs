import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import ts from 'typescript'

const graphSource = readFileSync(new URL('./src/graph.ts', import.meta.url), 'utf8')
const graphModule = await import(`data:text/javascript,${encodeURIComponent(ts.transpileModule(graphSource, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText)}`)

const member = (id, kind, name, namespace = '', cluster = 'org/edge-a') => ({
  id,
  cluster,
  object: { kind, apiVersion: 'apps/v1', metadata: { name, ...(namespace ? { namespace } : {}) } },
})

const topology = [
  {
    cluster: 'org/edge-a',
    relations: {
      members: [
        member('ns-apps', 'Namespace', 'apps'),
        member('deploy-web', 'Deployment', 'web', 'apps'),
        member('pod-web', 'Pod', 'web-1', 'apps'),
        member('node-a', 'Node', 'worker-a'),
      ],
    },
  },
  {
    cluster: 'org/edge-b',
    relations: { members: [member('deploy-api', 'Deployment', 'api', 'backend', 'org/edge-b')] },
  },
]

test('topology derivation preserves Edge → Namespace → Resource hierarchy', () => {
  const tree = graphModule.deriveTopologyTree(topology)

  assert.deepEqual(tree.edges.map((edge) => edge.name), ['edge-a', 'edge-b'])
  assert.equal(tree.edges[0].namespaces[0].name, 'apps')
  assert.equal(tree.edges[0].namespaces[0].resource.object.id, 'ns-apps')
  assert.deepEqual(tree.edges[0].namespaces[0].resources.map((row) => row.name), ['web', 'web-1'])
  assert.deepEqual(tree.edges[0].resources.map((row) => row.name), ['worker-a'])
  assert.deepEqual(tree.edges[1].namespaces.map((group) => group.name), ['backend'])
})

test('topology filters are pure and match visible resource rows', () => {
  const deployments = graphModule.deriveTopologyTree(topology, { kind: 'Deployment' })
  assert.deepEqual(deployments.edges.map((edge) => edge.name), ['edge-a', 'edge-b'])
  assert.equal(deployments.edges[0].namespaces[0].resource, undefined)
  assert.deepEqual(deployments.edges[0].namespaces[0].resources.map((row) => row.name), ['web'])
  assert.deepEqual(deployments.edges[1].namespaces[0].resources.map((row) => row.name), ['api'])

  const apps = graphModule.deriveTopologyTree(topology, { namespace: 'apps' })
  assert.deepEqual(apps.edges.map((edge) => edge.name), ['edge-a'])
  assert.equal(apps.edges[0].namespaces[0].resource.object.id, 'ns-apps')
  assert.deepEqual(apps.edges[0].namespaces[0].resources.map((row) => row.name), ['web', 'web-1'])

  const namespaces = graphModule.deriveTopologyTree(topology, { kind: 'Namespace' })
  assert.deepEqual(namespaces.edges.map((edge) => edge.name), ['edge-a'])
  assert.deepEqual(namespaces.edges[0].namespaces.map((group) => group.resource.object.id), ['ns-apps'])

  const none = graphModule.deriveTopologyTree(topology, { kind: 'Service' })
  assert.deepEqual(none.edges, [], 'a filter with no matches must not leave empty Edge groups')
})

test('topology graph elements mirror the filtered tree and keep cluster-scoped rows at Edge', () => {
  const filtered = graphModule.buildTopologyElements(topology, { kind: 'Deployment' })
  const nodes = filtered.elements
    .filter((element) => !element.data.source)
    .map((element) => element.data.id)

  assert.deepEqual(nodes, [
    'cluster:org/edge-a',
    'ns:org/edge-a/apps',
    'deploy-web',
    'cluster:org/edge-b',
    'ns:org/edge-b/backend',
    'deploy-api',
  ])
  assert.equal(filtered.nodeIndex['deploy-web'].object.metadata.namespace, 'apps')
  assert.equal(filtered.nodeIndex['deploy-api'].object.metadata.namespace, 'backend')

  const apps = graphModule.buildTopologyElements(topology, { namespace: 'apps' })
  const appNodeIds = apps.elements.filter((element) => !element.data.source).map((element) => element.data.id)
  assert.ok(appNodeIds.includes('ns-apps'))
  assert.ok(appNodeIds.includes('deploy-web'))
  assert.ok(appNodeIds.includes('pod-web'))
  assert.ok(!appNodeIds.includes('node-a'), 'namespace filtering must exclude cluster-scoped rows')
})

test('graph key actions and focus ownership are deterministic', () => {
  assert.equal(graphModule.graphKeyAction('ArrowUp'), 'pan-up')
  assert.equal(graphModule.graphKeyAction('d'), 'pan-right')
  assert.equal(graphModule.graphKeyAction('='), 'zoom-in')
  assert.equal(graphModule.graphKeyAction('Escape'), 'escape')
  assert.equal(graphModule.graphKeyAction('Tab'), null)

  const child = {}
  const other = {}
  const graph = { contains: (node) => node === child }
  assert.equal(graphModule.graphOwnsFocus(graph, graph), true)
  assert.equal(graphModule.graphOwnsFocus(graph, child), true)
  assert.equal(graphModule.graphOwnsFocus(graph, other), false)
  assert.equal(graphModule.graphOwnsFocus(graph, null), false)
})

test('mountGraph focuses the labeled container on pointer use and removes the listener on destroy', async () => {
  const previousWindow = globalThis.window
  let focusCount = 0
  let destroyed = 0
  let pointerListener
  const container = {
    focus: () => { focusCount += 1 },
    addEventListener: (type, listener) => { if (type === 'pointerdown') pointerListener = listener },
    removeEventListener: (type, listener) => {
      if (type === 'pointerdown' && listener === pointerListener) pointerListener = undefined
    },
  }
  const fakeCytoscape = () => ({
    on: () => {},
    destroy: () => { destroyed += 1 },
  })

  try {
    globalThis.window = { cytoscape: fakeCytoscape }
    const handle = await graphModule.mountGraph(container, [], [], () => {}, '/cytoscape.min.js')
    assert.equal(typeof pointerListener, 'function')
    pointerListener()
    assert.equal(focusCount, 1)
    handle.destroy()
    assert.equal(pointerListener, undefined)
    assert.equal(destroyed, 1)
  } finally {
    globalThis.window = previousWindow
  }
})

test('graph additions enforce a hard node limit while retaining parallel relation edges', async () => {
  const previousWindow = globalThis.window
  const stored = new Map([['root', { data: { id: 'root' } }]])
  const cy = {
    on: () => {},
    destroy: () => {},
    nodes: () => ({ length: [...stored.values()].filter(element => !element.data.source).length }),
    getElementById: id => ({ nonempty: () => stored.has(id), empty: () => !stored.has(id) }),
    add: elements => {
      for (const element of elements) {
        assert.ok(!stored.has(element.data.id), `duplicate ${element.data.id} must be removed before Cytoscape add`)
        stored.set(element.data.id, element)
      }
    },
  }

  try {
    globalThis.window = { cytoscape: () => cy }
    const handle = await graphModule.mountGraph({ addEventListener: () => {}, removeEventListener: () => {} }, [], [], () => {}, '/cytoscape.min.js')
    const added = handle.add([
      { data: { id: 'child', label: 'child' } },
      { data: { id: 'root>child:owners', source: 'root', target: 'child', rel: 'owners' } },
      { data: { id: 'child', label: 'duplicate child' } },
      { data: { id: 'root>child:references', source: 'root', target: 'child', rel: 'references' } },
      { data: { id: 'over-limit', label: 'over limit' } },
      { data: { id: 'root>over-limit', source: 'root', target: 'over-limit', rel: 'owners' } },
    ], 2)

    assert.deepEqual(added.map(element => element.data.id), ['child', 'root>child:owners', 'root>child:references'])
    assert.equal(stored.has('over-limit'), false)
    handle.destroy()
  } finally {
    globalThis.window = previousWindow
  }
})

test('topology switch and resource activation are native, labeled controls', () => {
  const source = readFileSync(new URL('./src/components/TopologyView.vue', import.meta.url), 'utf8')
  assert.match(source, /role="group" aria-label="Topology representation"/u)
  assert.match(source, /:aria-pressed="representation === value"/u)
  assert.match(source, /@click="emit\('inspect', row\.object\)"/u)
  assert.match(source, /role="region" aria-label="Fleet topology visualization"[^>]*tabindex="0"/u)
  assert.match(source, /FormSelect v-model="layout"/u)
  assert.match(source, /Reset graph/u)
})

test('filtered-empty topology renders a status before either representation', () => {
  const source = readFileSync(new URL('./src/components/TopologyView.vue', import.meta.url), 'utf8')
  const noClusters = source.indexOf('No clusters engaged')
  const noMatches = source.indexOf('No resources match the current topology filters')
  const list = source.indexOf("representation === 'list'")
  assert.ok(noClusters > 0 && noMatches > noClusters && list > noMatches)
})

test('graph keyboard routing is focus-scoped and fullscreen uses the shared layer token', () => {
  const elementSource = readFileSync(new URL('./src/components/TopologyView.vue', import.meta.url), 'utf8')
  const graphSource = readFileSync(new URL('./src/graph.ts', import.meta.url), 'utf8')
  const styleSource = readFileSync(new URL('./src/style.css', import.meta.url), 'utf8')
  const sharedStyleSource = readFileSync(new URL('./src/portalkit/faros-ui.css', import.meta.url), 'utf8')

  assert.match(elementSource, /@keydown="graphKeydown"/u)
  assert.match(elementSource, /if \(handled\) event\.preventDefault\(\)/u)
  assert.match(graphSource, /const focusGraph = \(\) => container\.focus\(\)/u)
  assert.match(graphSource, /container\.addEventListener\('pointerdown', focusGraph\)/u)
  assert.match(graphSource, /container\.removeEventListener\('pointerdown', focusGraph\)/u)
  assert.match(elementSource, /target\.requestFullscreen/u)
  assert.match(elementSource, /document\.fullscreenElement === panel\.value/u)
  assert.match(styleSource, /z-index: var\(--k-layer-fullscreen, 2000\)/u)
  assert.match(sharedStyleSource, /--k-layer-fullscreen: 2000/u)
})

test('relation metadata is the shared source for labels, direction, and graph colors', () => {
  const metadata = graphModule.RELATION_METADATA
  const luminance = (color) => {
    const channels = [1, 3, 5].map(index => Number.parseInt(color.slice(index, index + 2), 16) / 255)
      .map(value => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4)
    return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2]
  }
  const contrast = (foreground, background) => {
    const [lighter, darker] = [luminance(foreground), luminance(background)].sort((a, b) => b - a)
    return (lighter + 0.05) / (darker + 0.05)
  }
  assert.ok(metadata.length > 0)
  assert.deepEqual(graphModule.IMPACT_RELATIONS, metadata.map(({ name }) => name))
  for (const relation of metadata) {
    assert.equal(graphModule.RELATION_COLORS[relation.name], relation.color)
    assert.equal(graphModule.RELATION_LABELS[relation.name], relation.label)
    assert.equal(graphModule.RELATION_DIR[relation.name], relation.direction)
    assert.match(relation.lightColor, /^#[0-9a-f]{6}$/iu)
    assert.ok(contrast(relation.lightColor, '#f1f1f6') >= 3, `${relation.name} needs 3:1 contrast on the light graph surface`)
    assert.ok(['solid', 'dashed', 'dotted'].includes(relation.lineStyle))
    assert.ok(relation.description.length > 0)
  }
})

test('impact graph keeps distinct declared relations between the same objects', () => {
  const related = (id, kind) => ({
    id,
    cluster: 'org/edge-a',
    object: { kind, apiVersion: 'v1', metadata: { name: id } },
  })
  const built = graphModule.buildElements({
    id: 'pod-1',
    cluster: 'org/edge-a',
    object: { kind: 'Pod', apiVersion: 'v1', metadata: { name: 'api' } },
    relations: {
      owners: [related('controller-1', 'Deployment')],
      references: [related('controller-1', 'Deployment')],
    },
  })
  const relationEdges = built.elements.filter(element => element.data.source)

  assert.equal(relationEdges.length, 2)
  assert.deepEqual(relationEdges.map(element => element.data.rel), ['owners', 'references'])
  assert.ok(relationEdges.every(element => element.data.edgeLabel))
  assert.equal(new Set(relationEdges.map(element => element.data.id)).size, relationEdges.length, 'each legend relation needs its own graph edge')
})

test('topology and impact disclose bounded results without conflating response truncation', () => {
  const topology = readFileSync(new URL('./src/components/TopologyView.vue', import.meta.url), 'utf8')
  const impact = readFileSync(new URL('./src/components/ImpactView.vue', import.meta.url), 'utf8')

  assert.match(topology, /const requestGeneration = \+\+loadGeneration/u)
  assert.match(topology, /loadGeneration !== requestGeneration/u)
  assert.match(impact, /const requestGeneration = \+\+loadGeneration/u)
  assert.match(impact, /loadGeneration !== requestGeneration/u)
  assert.match(topology, /up to 1,000 members per edge/u)
  assert.match(topology, /depth 5/u)
  assert.match(topology, /200 objects for Namespace membership/u)
  assert.match(topology, /4,000 nodes or 30 rounds/u)
  assert.match(impact, /depth 5/u)
  assert.match(impact, /at most 200 related objects/u)
  assert.match(topology, /does not identify relation-level bounds/u)
  assert.match(impact, /does not identify relation-level bounds/u)
  assert.doesNotMatch(topology, /Select one edge for a complete view/u)
  assert.match(impact, /not a complete relation traversal/u)
})

test('CSS fullscreen fallback has a deterministic exit after rejected native requests', () => {
  const source = readFileSync(new URL('./src/components/TopologyView.vue', import.meta.url), 'utf8')
  assert.match(source, /if \(!document\.fullscreenElement && full\.value\) full\.value = false/u)
  assert.match(source, /catch \{ if \([^}]*!document\.fullscreenElement\) full\.value = true \}/u)
})

test('impact view exposes a semantic relation legend from shared metadata', () => {
  const source = readFileSync(new URL('./src/components/ImpactView.vue', import.meta.url), 'utf8')
  assert.match(source, /RELATION_METADATA/u)
  assert.match(source, /<aside[^>]+aria-labelledby="impact-legend-title"/u)
  assert.match(source, /<dl class="legend">/u)
  assert.match(source, /v-for="relation in legendRelations"/u)
  assert.match(source, /class="legend-swatch"[^>]+borderTopColor: relation\.displayColor[^>]+borderTopStyle: relation\.lineStyle/u)
  assert.doesNotMatch(source, /kuery-impact-legend kuery-panel k-card/u)
})

test('graph rendering provides theme contrast, relation labels, resize handling, and bounded expansion control', () => {
  const graphSource = readFileSync(new URL('./src/graph.ts', import.meta.url), 'utf8')
  const topologySource = readFileSync(new URL('./src/components/TopologyView.vue', import.meta.url), 'utf8')

  assert.match(graphSource, /label: 'data\(edgeLabel\)'/u)
  assert.match(graphSource, /'line-style': relation\.lineStyle/u)
  assert.match(graphSource, /new ResizeObserver/u)
  assert.match(graphSource, /resizeObserver\?\.disconnect\(\)/u)
  assert.doesNotMatch(graphSource, /wheelSensitivity/u)
  assert.match(topologySource, /Cancel expansion/u)
  assert.match(topologySource, /handle\.add\(built\.elements, 4000\)/u)
  assert.match(topologySource, /handle\.hasNode\(id\)[\s\S]*handle\.isExpanded\(id\)/u)
  assert.match(topologySource, /if \(handle\.nodeCount\(\) >= 4000\) break/u)
  assert.match(topologySource, /if \(handle\.hasNode\(key\)\) graphObjects\.set\(key, row\)/u)
  assert.match(graphSource, /expandedFrom: anchorId/u)
  assert.match(graphSource, /edge\.data\('expandedFrom'\) === parentID/u)
  assert.match(topologySource, /await new Promise<void>\(resolve => requestAnimationFrame/u)
  assert.match(topologySource, /\(rounds \+ 1\) % 3 === 0/u)
  assert.match(topologySource, /aria-live="polite" aria-atomic="true"/u)
})
