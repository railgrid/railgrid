import type { TableFilterState } from './portalkit/table'

export interface InstanceListRequest {
  mode: 'server' | 'client'
  active: boolean
  page: number
  pageSize: number
  query: string
  filters: TableFilterState
  cursor: string | null
}

/**
 * A client-mode list is a complete, query-independent authority read. Its
 * result can therefore commit after local query/filter edits, while server
 * page reads must remain tied to the exact requested filter shape.
 *
 * Page-size, page, cursor, mode, and active-state changes remain request
 * authority changes. The refresh generation is checked separately so queued
 * mutations, context changes, and unmounts still invalidate the read.
 */
export function isCurrentInstanceListRequest(
  request: InstanceListRequest,
  current: InstanceListRequest,
  refreshCurrent: boolean,
): boolean {
  if (!refreshCurrent || current.mode !== request.mode || current.active !== request.active) return false
  if (current.page !== request.page || current.pageSize !== request.pageSize || current.cursor !== request.cursor) return false
  if (request.mode === 'client') return true
  return current.query === request.query
    && current.filters.template === request.filters.template
    && current.filters.status === request.filters.status
}
