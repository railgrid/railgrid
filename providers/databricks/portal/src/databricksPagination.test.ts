import type { Connection, Warehouse } from './types.js'
import {
  databricksHybridTransition,
  databricksServerPageTransition,
  serverCursorChange,
  tableFilters,
  warehouseFilters,
} from './databricksPagination.js'

function equal(actual: unknown, expected: unknown, label: string) {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(`${label}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`)
  }
}

const manyConnections = Array.from({ length: 101 }, (_, index) => ({ name: `connection-${index}` })) as unknown as Connection[]
const manyWarehouses = Array.from({ length: 101 }, (_, index) => ({ name: `warehouse-${index}` })) as unknown as Warehouse[]
const warehouseConnectionFilter = warehouseFilters(manyConnections).find(filter => filter.key === 'connectionRef')
const tableWarehouseFilter = tableFilters(manyWarehouses).find(filter => filter.key === 'warehouseRef')
equal(warehouseConnectionFilter?.options?.length, 101, 'warehouse filter retains every supporting connection choice')
equal(warehouseConnectionFilter?.options?.some(option => option.value === 'connection-100'), true, 'warehouse filter includes a connection after item 100')
equal(tableWarehouseFilter?.options?.length, 101, 'table filter retains every supporting warehouse choice')
equal(tableWarehouseFilter?.options?.some(option => option.value === 'warehouse-100'), true, 'table filter includes a warehouse after item 100')

equal(
  serverCursorChange({ reason: 'page', page: 3, cursor: 'opaque-page-3' }),
  { page: 3, cursor: 'opaque-page-3' },
  'server page events preserve page and cursor',
)
equal(
  serverCursorChange({ reason: 'page-size', page: 1, cursor: null }),
  { page: 1, cursor: null },
  'page-size events preserve the table reset',
)
equal(
  serverCursorChange({ reason: 'query', page: 4, cursor: 'stale-query-cursor' }),
  { page: 1, cursor: null },
  'clearing a query resets server pagination',
)
equal(
  serverCursorChange({ reason: 'filter', page: 2, cursor: 'stale-filter-cursor' }),
  { page: 1, cursor: null },
  'clearing a filter resets server pagination',
)

equal(
  databricksHybridTransition({
    mode: 'server',
    active: false,
    completeFirstPage: true,
    fullWalkPending: false,
  }),
  { mode: 'server', reload: true, clearRows: true, reuseRows: false },
  'an inactive terminal page remains server-owned',
)
equal(
  databricksHybridTransition({
    mode: 'server',
    active: true,
    completeFirstPage: true,
    fullWalkPending: false,
  }),
  { mode: 'client', reload: false, clearRows: false, reuseRows: true },
  'an active query reuses a complete first page without a full walk',
)
equal(
  databricksHybridTransition({
    mode: 'server',
    active: true,
    completeFirstPage: false,
    fullWalkPending: false,
  }),
  { mode: 'server', reload: true, clearRows: true, reuseRows: false },
  'an active query starts one complete walk from an incomplete page',
)
equal(
  databricksHybridTransition({
    mode: 'server',
    active: true,
    completeFirstPage: false,
    fullWalkPending: true,
  }),
  { mode: 'server', reload: false, clearRows: false, reuseRows: false },
  'rapid active edits coalesce onto the pending complete walk',
)
equal(
  databricksHybridTransition({
    mode: 'client',
    active: false,
    completeFirstPage: false,
    fullWalkPending: false,
  }),
  { mode: 'server', reload: true, clearRows: true, reuseRows: false },
  'clearing an active query returns to server page one authority',
)
const clearedAuthority = databricksHybridTransition({
  mode: 'client',
  active: false,
  completeFirstPage: false,
  fullWalkPending: false,
})
equal(
  databricksHybridTransition({
    mode: clearedAuthority.mode,
    active: true,
    completeFirstPage: false,
    fullWalkPending: false,
  }),
  { mode: 'server', reload: true, clearRows: true, reuseRows: false },
  'a later search cannot reuse the pre-clear client source without a new walk',
)

equal(
  databricksServerPageTransition({
    active: true,
    page: 1,
    cursor: null,
    pageInfo: { hasNext: false, nextCursor: null },
  }),
  { assignRows: true, promoteToClient: true, startFullWalk: false },
  'an old complete first page may be promoted without flashing unfiltered rows',
)
equal(
  databricksServerPageTransition({
    active: true,
    page: 1,
    cursor: null,
    pageInfo: { hasNext: true, nextCursor: 'page-2' },
  }),
  { assignRows: false, promoteToClient: false, startFullWalk: true },
  'an old incomplete first page is discarded before the active full walk',
)
equal(
  databricksServerPageTransition({
    active: false,
    page: 2,
    cursor: 'page-2',
    pageInfo: { hasNext: false, nextCursor: null },
  }),
  { assignRows: true, promoteToClient: false, startFullWalk: false },
  'inactive server pages remain assignable server authority',
)
