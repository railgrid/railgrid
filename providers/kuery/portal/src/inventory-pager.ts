import type { InventoryFilters } from './api'
import type { ResourceTableChange, TablePageInfo } from './portalkit/table'

export interface InventoryPagerState {
  page: number
  pageSize: number
  query: string
  filters: Record<string, string>
  cursor: string | null
  nextCursor: string | null
  pageCursors: Array<string | null | undefined>
  pageInfo: TablePageInfo
  paginationGap: boolean
  requestID: number
}

export interface InventoryRequest {
  id: number
  pageSize: number
  cursor: string | null
  filters: InventoryFilters
}

export function normalizeInventoryFilter(value: string | null | undefined): string {
  return value?.trim() ?? ''
}

export function createInventoryPager(pageSize = 50): InventoryPagerState {
  return { page: 1, pageSize, query: '', filters: {}, cursor: null, nextCursor: null, pageCursors: [null], pageInfo: { hasNext: false, nextCursor: null, total: null }, paginationGap: false, requestID: 0 }
}

export function changeInventoryPager(state: InventoryPagerState, change: ResourceTableChange): InventoryPagerState {
  const shapeChanged = change.reason !== 'page'
  if (shapeChanged) {
    const filters = Object.fromEntries(Object.entries(change.filters).map(([key, value]) => [key, normalizeInventoryFilter(value)]))
    return { ...state, page: 1, pageSize: change.pageSize, query: change.query, filters, cursor: null, nextCursor: null, pageCursors: [null], pageInfo: { hasNext: false, nextCursor: null, total: null }, paginationGap: false }
  }
  const pageCursors = [...state.pageCursors]
  while (pageCursors.length < change.page) pageCursors.push(undefined)
  const saved = pageCursors[change.page - 1]
  const cursor = change.cursor ?? saved ?? null
  pageCursors[change.page - 1] = cursor
  return { ...state, page: change.page, cursor, nextCursor: null, pageCursors, pageInfo: { ...state.pageInfo, hasNext: false, nextCursor: null }, paginationGap: false }
}

export function beginInventoryRequest(state: InventoryPagerState): { state: InventoryPagerState; request: InventoryRequest } {
  const id = state.requestID + 1
  const filters: InventoryFilters = {
    edge: normalizeInventoryFilter(state.filters.edge) || undefined,
    kind: normalizeInventoryFilter(state.filters.kind) || undefined,
    namespace: normalizeInventoryFilter(state.filters.namespace) || undefined,
    // PortalKit's standardized search field is exact because Kuery's server
    // contract has no substring operator. Whitespace is not cursor state.
    name: state.query.trim() || undefined,
  }
  return { state: { ...state, requestID: id }, request: { id, pageSize: state.pageSize, cursor: state.cursor, filters } }
}

export function applyInventoryPage(
  state: InventoryPagerState,
  requestID: number,
  page: { nextCursor: string | null; total: number | null; hasNext: boolean },
): InventoryPagerState {
  if (requestID !== state.requestID) return state
  const countHasMore = page.total !== null && state.page * state.pageSize < page.total
  const countHasNext = page.total === null ? page.hasNext : countHasMore
  const hasNext = countHasNext && page.nextCursor !== null
  const pageCursors = [...state.pageCursors]
  if (hasNext) pageCursors[state.page] = page.nextCursor
  return {
    ...state,
    nextCursor: hasNext ? page.nextCursor : null,
    pageCursors,
    pageInfo: { hasNext, nextCursor: hasNext ? page.nextCursor : null, total: page.total },
    paginationGap: countHasMore && page.nextCursor === null,
  }
}

export function isCurrentInventoryRequest(state: InventoryPagerState, requestID: number): boolean {
  return state.requestID === requestID
}
