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

// A discrete Cytoscape layout emits layoutstop synchronously inside run().
const discreteLayoutStub = () => {
  let onStop
  return { one: (event, listener) => { if (event === 'layoutstop') onStop = listener }, run() { onStop?.() }, stop() {} }
}

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
    nodes: () => ({ length: 0 }),
    layout: discreteLayoutStub,
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
    layout: discreteLayoutStub,
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

test('force layout is sized to the graph and never runs a large graph synchronously', () => {
  const small = graphModule.forceLayoutSizing(50)
  assert.equal(small.animate, false, 'a small graph lays out synchronously for a stable, immediate result')
  assert.equal(small.numIter, 1000)
  assert.equal(small.refresh, 20)

  const fleet = graphModule.forceLayoutSizing(2000)
  assert.equal(fleet.animate, true, 'a fleet-sized graph must step across animation frames, not block the tab')
  assert.ok(fleet.numIter >= 100 && fleet.numIter < small.numIter, `iterations shrink with size, got ${fleet.numIter}`)
  assert.ok(fleet.refresh >= 1 && fleet.refresh <= 4, `few iterations per frame on 2,000 nodes, got ${fleet.refresh}`)

  // Total work stays bounded: pair evaluations per run never exceed the budget
  // by more than the iteration floor allows, and shrink monotonically.
  let previous = Number.POSITIVE_INFINITY
  for (const nodes of [100, 300, 500, 1000, 2000, 4000]) {
    const sizing = graphModule.forceLayoutSizing(nodes)
    assert.ok(sizing.numIter <= previous, `iterations must not grow with node count (${nodes})`)
    assert.ok(sizing.refresh >= 1 && sizing.refresh <= 20)
    // The cooling schedule reaches minTemp exactly at numIter, so a shortened
    // run settles instead of being cut off hot.
    const finalTemp = 1000 * Math.pow(sizing.coolingFactor, sizing.numIter)
    assert.ok(Math.abs(finalTemp - 1) < 0.01, `cooling must reach minTemp at numIter (${nodes}: ${finalTemp})`)
    previous = sizing.numIter
  }
  assert.equal(graphModule.forceLayoutSizing(graphModule.FORCE_SYNC_NODE_LIMIT + 1).animate, true)
  assert.equal(graphModule.forceLayoutSizing(graphModule.FORCE_SYNC_NODE_LIMIT).animate, false)
})

test('force layout options randomize only a fresh graph and carry the sizing', () => {
  const fresh = graphModule.forceLayoutOptions(800, { incremental: false })
  assert.equal(fresh.name, 'cose')
  assert.equal(fresh.randomize, true)
  assert.equal(fresh.animate, true)
  assert.equal(fresh.fit, true)
  assert.equal(fresh.initialTemp, 1000)
  assert.equal(fresh.minTemp, 1)
  assert.deepEqual(
    { numIter: fresh.numIter, refresh: fresh.refresh, coolingFactor: fresh.coolingFactor },
    (({ numIter, refresh, coolingFactor }) => ({ numIter, refresh, coolingFactor }))(graphModule.forceLayoutSizing(800)),
  )
  assert.equal(graphModule.forceLayoutOptions(800).randomize, false, 'relayout keeps current positions as the start')
})

test('topology view keeps the force layout off the main thread and stoppable', () => {
  const graphSource = readFileSync(new URL('./src/graph.ts', import.meta.url), 'utf8')
  const topologySource = readFileSync(new URL('./src/components/TopologyView.vue', import.meta.url), 'utf8')

  // The view never hands Cytoscape a synchronous cose config of its own.
  assert.doesNotMatch(topologySource, /name: 'cose'/u)
  assert.match(topologySource, /forceLayoutOptions\(/u)
  assert.match(topologySource, /layoutConfig\(\{ incremental: false, nodeCount \}\)/u)
  // Expand all runs the force layout once, over the final graph.
  assert.match(topologySource, /\(rounds \+ 1\) % 3 === 0 && layoutDirty && layout\.value !== 'cose'/u)
  // Users can stop a running layout and see that it is running.
  assert.match(topologySource, /v-if="layoutRunning"[^>]*@click="stopLayout">Stop layout</u)
  assert.match(topologySource, /Force layout is settling/u)
  // The handle tracks one layout at a time, stops it before starting another
  // or tearing down, and reports activity to the view.
  assert.match(graphSource, /running\?\.stop\(\)\s+const next = cy\.layout\(/u)
  assert.match(graphSource, /destroy: \(\) => \{\s+running\?\.stop\(\)/u)
  assert.match(graphSource, /next\.one\('layoutstop'/u)
  assert.match(graphSource, /layout: \{ name: 'preset' \}/u)
  assert.match(graphSource, /hooks\?\.onLayout\?\.\(true, cy\.nodes\(\)\.length\)/u)
})

test('mountGraph runs one layout at a time, reports activity, and stops a running layout on relayout and destroy', async () => {
  const previousWindow = globalThis.window
  const layouts = []
  // An animated layout: run() returns without emitting layoutstop; the test
  // drives completion (or stop) by calling finish().
  const animatedLayout = config => {
    const layout = { config, runs: 0, stops: 0, onStop: undefined }
    layout.one = (event, listener) => { if (event === 'layoutstop') layout.onStop = listener }
    layout.run = () => { layout.runs += 1 }
    layout.stop = () => { layout.stops += 1 }
    layout.finish = () => layout.onStop?.()
    layouts.push(layout)
    return layout
  }
  const cy = { on: () => {}, destroy: () => {}, nodes: () => ({ length: 3 }), layout: animatedLayout }
  const activity = []

  try {
    globalThis.window = { cytoscape: () => cy }
    const handle = await graphModule.mountGraph(
      { addEventListener: () => {}, removeEventListener: () => {} }, [], [], () => {}, '/cytoscape.min.js',
      { name: 'cose', animate: true },
      { onLayout: (running, nodes) => activity.push([running, nodes]) },
    )
    assert.equal(layouts.length, 1)
    assert.equal(layouts[0].runs, 1)
    assert.equal(handle.layoutRunning(), true)
    assert.deepEqual(activity, [[true, 3]])

    // A relayout while the first is still stepping stops the first; the
    // first's late layoutstop must not be mistaken for the second finishing.
    const second = handle.relayout({ name: 'cose', animate: true })
    assert.equal(layouts[0].stops, 1)
    assert.equal(layouts.length, 2)
    layouts[0].finish()
    assert.equal(handle.layoutRunning(), true, 'the superseded layout stopping does not end the current one')
    assert.deepEqual(activity, [[true, 3], [true, 3]])

    layouts[1].finish()
    await second
    assert.equal(handle.layoutRunning(), false)
    assert.deepEqual(activity.at(-1), [false, 3])

    // Stop halts the current layout where it is; teardown stops whatever runs.
    void handle.relayout({ name: 'cose', animate: true })
    handle.stopLayout()
    assert.equal(layouts[2].stops, 1)
    layouts[2].finish()
    assert.equal(handle.layoutRunning(), false)
    void handle.relayout({ name: 'cose', animate: true })
    handle.destroy()
    assert.equal(layouts[3].stops, 1)
  } finally {
    globalThis.window = previousWindow
  }
})
