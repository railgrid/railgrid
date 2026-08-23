import type { Connection, Repository } from './types'
import type { ResourceTableChange, TableFilterDefinition, TableFilterOption, TablePageInfo } from './portalkit/table'

/** The default page shown while the repository table has no active filters. */
export const REPOSITORY_PAGE_SIZE = 10

export type RepositoryPaginationMode = 'server' | 'client'

export interface RepositoryPaginationState {
  mode: RepositoryPaginationMode
  page: number
  pageSize: number
  query: string
  filters: RepositoryFilterValues
  cursor: string | null
}

export interface RepositoryFilterValues {
  connectionRef: string
  visibility: string
  status: string
}

export const EMPTY_REPOSITORY_FILTERS: RepositoryFilterValues = {
  connectionRef: '',
  visibility: '',
  status: '',
}

// These values are part of the API contract. Keep the options explicit: a
// cursor page cannot provide a complete vocabulary for a server-side filter.
export const REPOSITORY_VISIBILITY_OPTIONS: TableFilterOption[] = [
  { value: 'private', label: 'Private' },
  { value: 'public', label: 'Public' },
  { value: 'internal', label: 'Internal' },
]

export const REPOSITORY_STATUS_OPTIONS: TableFilterOption[] = [
  { value: 'ready', label: 'Ready' },
  { value: 'pending', label: 'Pending' },
  { value: 'failed', label: 'Failed' },
  { value: 'Deleting', label: 'Deleting' },
]

function resourceOptions(values: readonly string[]): TableFilterOption[] {
  return [...new Set(values.filter(Boolean))]
    .sort((left, right) => left.localeCompare(right, undefined, { numeric: true, sensitivity: 'base' }))
    .map(value => ({ value, label: value }))
}

/**
 * Build the repository filter definitions from the complete connection read.
 * Connection names must not be inferred from the currently visible page of
 * repositories, because a cursor page is only a partial repository source.
 */
export function repositoryFilters(connections: readonly Connection[]): TableFilterDefinition[] {
  return [
    {
      key: 'connectionRef',
      label: 'Connection',
      options: resourceOptions(connections.map(connection => connection.name)),
    },
    {
      key: 'visibility',
      label: 'Visibility',
      options: REPOSITORY_VISIBILITY_OPTIONS,
    },
    {
      key: 'status',
      label: 'Status',
      allLabel: 'Any status',
      options: REPOSITORY_STATUS_OPTIONS,
    },
  ]
}

export function cloneRepositoryFilters(filters: RepositoryFilterValues): RepositoryFilterValues {
  return {
    connectionRef: filters.connectionRef,
    visibility: filters.visibility,
    status: filters.status,
  }
}

export function hasActiveRepositoryFilters(query: string, filters: RepositoryFilterValues): boolean {
  return !!query.trim() || Object.values(filters).some(Boolean)
}

export interface RepositoryPaginationChangeResult {
  state: RepositoryPaginationState
  reload: boolean
  clearRows: boolean
}

/**
 * Apply one controlled ResourceTable change without interpreting cursor
 * values. Server page navigation is a normal read and must retain the opaque
 * page/cursor emitted by the table. Query/filter/page-size changes reset the
 * cursor boundary; clearing a client-side query returns to server page one.
 */
export function applyRepositoryPaginationChange(
  previous: RepositoryPaginationState,
  change: ResourceTableChange,
): RepositoryPaginationChangeResult {
  const filters: RepositoryFilterValues = {
    connectionRef: change.filters.connectionRef || '',
    visibility: change.filters.visibility || '',
    status: change.filters.status || '',
  }
  const active = hasActiveRepositoryFilters(change.query, filters)
  const preserveServerPage = !active && previous.mode === 'server' && change.reason === 'page'
  const state: RepositoryPaginationState = {
    mode: previous.mode,
    page: preserveServerPage ? change.page : active ? change.page : 1,
    pageSize: change.pageSize,
    query: change.query,
    filters,
    cursor: preserveServerPage ? change.cursor : active ? change.cursor : null,
  }

  if (!active) {
    state.mode = 'server'
    return { state, reload: true, clearRows: true }
  }

  // Once a complete list is loaded, ResourceTable handles active query/filter
  // and page changes locally. The server -> client transition is the only
  // active state that needs another read.
  if (previous.mode === 'client') return { state, reload: false, clearRows: false }
  return { state, reload: true, clearRows: true }
}

export interface RepositoryPageInfo extends TablePageInfo {
  hasNext: boolean
  nextCursor: string | null
}

/** Convert an opaque API continue token to ResourceTable's cursor metadata. */
export function repositoryPageInfo(nextCursor?: string): RepositoryPageInfo {
  const cursor = nextCursor || null
  return { hasNext: cursor !== null, nextCursor: cursor }
}

/**
 * Map observed Repository state to the values used by the table filter and
 * StatusBadge. A controller error is failed only after it has observed the
 * current generation; a controller that has not observed the object yet is
 * still pending even though the API exposes a waiting message.
 */
export function repositoryStatus(repository: Pick<Repository, 'deletionTimestamp' | 'generation' | 'observedGeneration' | 'ready' | 'message'>): string {
  if (repository.deletionTimestamp) return 'Deleting'
  if (repository.generation !== undefined &&
    (repository.observedGeneration === undefined || repository.observedGeneration < repository.generation)) {
    return 'pending'
  }
  if (repository.ready) return 'ready'
  if (repository.message) return 'failed'
  return 'pending'
}
