import {
  appendTreePage,
  emptyRegistrationTree,
  exhaustBranchSelection,
  treeCheckState,
  updateLeafSelection,
  visibleTreeIds,
  type RegistrationTreeNode,
} from './registrationTree.js'

function node(id: string, kind: RegistrationTreeNode['kind'], parentId?: string, disabled = false): RegistrationTreeNode {
  return { id, kind, label: id, parentId, depth: parentId ? (kind === 'table' ? 3 : 2) : 1, disabled, expanded: false, childrenLoaded: kind === 'table' || kind === 'warehouse', loading: false, childIds: [] }
}
function equal(actual: unknown, expected: unknown, label: string) { if (JSON.stringify(actual) !== JSON.stringify(expected)) throw new Error(`${label}: ${JSON.stringify(actual)}`) }

const state = emptyRegistrationTree()
appendTreePage(state, undefined, { nodes: [node('catalog', 'catalog')] })
appendTreePage(state, 'catalog', { nodes: [node('schema', 'schema', 'catalog')] })
appendTreePage(state, 'schema', { nodes: [node('table-a', 'table', 'schema'), node('table-b', 'table', 'schema', true)] })
state.nodes.catalog.expanded = true
state.nodes.schema.expanded = true
equal(visibleTreeIds(state), ['catalog', 'schema', 'table-a', 'table-b'], 'visible hierarchy')
equal(treeCheckState(state, 'catalog'), 'false', 'unchecked branch')
equal(updateLeafSelection(state, 'table-a', true), null, 'leaf selection succeeds')
equal(treeCheckState(state, 'catalog'), 'true', 'disabled leaves excluded from branch state')
appendTreePage(state, 'schema', { nodes: [node('table-c', 'table', 'schema')], nextPageToken: 'more' })
equal(treeCheckState(state, 'catalog'), 'mixed', 'unloaded pages prevent a fully checked branch')
equal(state.selectedLeafIds, ['table-a'], 'pagination preserves stable leaf selection')
equal(updateLeafSelection(state, 'table-c', true, 1), 'Register at most 1 resources at a time.', 'leaf limit is enforced')

const disabledBranch = emptyRegistrationTree()
appendTreePage(disabledBranch, undefined, { nodes: [node('disabled-catalog', 'catalog')] })
appendTreePage(disabledBranch, 'disabled-catalog', {
  nodes: [node('supported-schema', 'schema', 'disabled-catalog'), node('unsupported-schema', 'schema', 'disabled-catalog', true)],
})
appendTreePage(disabledBranch, 'supported-schema', { nodes: [node('supported-table', 'table', 'supported-schema')] })
disabledBranch.selectedLeafIds = ['supported-table']
equal(treeCheckState(disabledBranch, 'disabled-catalog'), 'true', 'disabled branches do not make a fully selected branch appear mixed')

const lazy = emptyRegistrationTree()
appendTreePage(lazy, undefined, { nodes: [node('c', 'catalog')] })
let calls = 0
const complete = await exhaustBranchSelection(lazy, 'c', async (parent, token) => {
  calls += 1
  if (parent.kind === 'catalog') return { nodes: [node('s', 'schema', 'c')] }
  return token ? { nodes: [node('t2', 'table', 's')] } : { nodes: [node('t1', 'table', 's')], nextPageToken: 'next' }
}, 50)
equal([complete.complete, complete.leafIds, calls, lazy.selectedLeafIds], [true, ['t1', 't2'], 3, []], 'atomic complete selection')
equal(complete.state?.selectedLeafIds, ['t1', 't2'], 'complete selection committed only to returned snapshot')

const overflow = await exhaustBranchSelection(lazy, 'c', async parent => parent.kind === 'catalog'
  ? { nodes: [node('s', 'schema', 'c')] }
  : { nodes: [node('t1', 'table', 's'), node('t2', 'table', 's')] }, 1)
equal([overflow.complete, overflow.leafIds, overflow.state], [false, [], undefined], 'overflow changes nothing')

let overflowCalls = 0
const earlyOverflow = await exhaustBranchSelection(lazy, 'c', async parent => {
  overflowCalls += 1
  if (parent.kind === 'catalog') return { nodes: [node('s', 'schema', 'c')] }
  return { nodes: [node('t1', 'table', 's'), node('t2', 'table', 's')], nextPageToken: 'must-not-load' }
}, 1)
equal([earlyOverflow.complete, earlyOverflow.pages, overflowCalls, lazy.selectedLeafIds], [false, 2, 2, []], 'overflow stops immediately after the first excessive page')

const bounded = await exhaustBranchSelection(lazy, 'c', async parent => parent.kind === 'catalog'
  ? { nodes: [node('s', 'schema', 'c')] }
  : { nodes: [], nextPageToken: 'again' }, 50, 2)
equal([bounded.complete, bounded.leafIds, bounded.pages], [false, [], 2], 'page cap fails atomically')

const failed = await exhaustBranchSelection(lazy, 'c', async () => { throw new Error('temporary discovery failure') }, 50)
equal([failed.complete, failed.leafIds, failed.state, lazy.selectedLeafIds], [false, [], undefined, []], 'discovery error changes nothing')
