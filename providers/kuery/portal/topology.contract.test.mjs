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

test('topology switch and resource activation are native, labeled controls', () => {
  const elementSource = readFileSync(new URL('./src/element.ts', import.meta.url), 'utf8')
  assert.match(elementSource, /data-topology-view=/u)
  assert.match(elementSource, /role="group" aria-label="Topology representation"/u)
  assert.match(elementSource, /aria-pressed="\$\{this\._topoView === view\}"/u)
  assert.match(elementSource, /this\._topoView = view[\s\S]{0,120}this\._render\(\)/u)
  assert.match(elementSource, /data-topology-resource=/u)
  assert.match(elementSource, /const row = this\._topologyListObjects\.get\(key\)/u)
  assert.match(elementSource, /button\.addEventListener\('click'/u)
  assert.match(elementSource, /if \(row\) void this\._runImpact\(row\)/u)
  assert.match(elementSource, /role="region" aria-label="Fleet topology graph"[^>]*tabindex="0"/u)
})

test('filtered-empty topology renders a status before either representation', () => {
  const elementSource = readFileSync(new URL('./src/element.ts', import.meta.url), 'utf8')
  assert.match(elementSource, /const topologyTree = this\._topology\.length[\s\S]{0,180}deriveTopologyTree\(this\._topology/u)
  assert.match(elementSource, /else if \(!hasTopologyRows\)[\s\S]{0,180}no resources match the current topology filters/u)
  assert.match(elementSource, /else if \(this\._topology\.length === 0\) \{\s*body = `[^`]*no clusters engaged/u)
  assert.match(elementSource, /else if \(!hasTopologyRows\)[\s\S]{0,240}else if \(this\._topoView === 'list'\)/u)
})

test('graph keyboard routing is focus-scoped and fullscreen uses the shared layer token', () => {
  const elementSource = readFileSync(new URL('./src/element.ts', import.meta.url), 'utf8')
  const graphSource = readFileSync(new URL('./src/graph.ts', import.meta.url), 'utf8')
  const styleSource = readFileSync(new URL('./src/style.css', import.meta.url), 'utf8')
  const sharedStyleSource = readFileSync(new URL('./src/portalkit/faros-ui.css', import.meta.url), 'utf8')

  assert.match(elementSource, /if \(!graphOwnsFocus\(graph, document\.activeElement\)\) return/u)
  assert.match(elementSource, /if \(handled\) ev\.preventDefault\(\)/u)
  assert.match(graphSource, /const focusGraph = \(\) => container\.focus\(\)/u)
  assert.match(graphSource, /container\.addEventListener\('pointerdown', focusGraph\)/u)
  assert.match(graphSource, /container\.removeEventListener\('pointerdown', focusGraph\)/u)
  assert.match(elementSource, /panel\.requestFullscreen\(\)\.catch\(\(\) => this\._toggleFullCSS\(panel\)\)/u)
  assert.match(elementSource, /this\._toggleFullCSS\(panel\)/u)
  assert.match(styleSource, /z-index: var\(--k-layer-fullscreen, 2000\)/u)
  assert.match(sharedStyleSource, /--k-layer-fullscreen: 2000/u)
})
