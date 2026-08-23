import type { ResourceTableChange, TableFilterDefinition } from './portalkit/table'

export const PACKAGE_PAGE_SIZE = 10

// GitHub's package API supports these six ecosystems. Keep the options
// explicit because server pagination only supplies one page to ResourceTable;
// deriving options from that page would make filters silently incomplete.
export const PACKAGE_FILTERS: TableFilterDefinition[] = [
  {
    key: 'type',
    label: 'Type',
    options: [
      { value: 'container', label: 'Container' },
      { value: 'docker', label: 'Docker' },
      { value: 'npm', label: 'npm' },
      { value: 'maven', label: 'Maven' },
      { value: 'rubygems', label: 'RubyGems' },
      { value: 'nuget', label: 'NuGet' },
    ],
  },
  {
    key: 'visibility',
    label: 'Visibility',
    options: [
      { value: 'public', label: 'Public' },
      { value: 'internal', label: 'Internal' },
      { value: 'private', label: 'Private' },
      // GitHub may omit visibility for a package while it is being crawled.
      { value: 'unknown', label: 'Unknown' },
    ],
  },
  {
    key: 'status',
    label: 'Status',
    allLabel: 'Any status',
    options: [
      { value: 'ready', label: 'Ready' },
      { value: 'pending', label: 'Pending' },
      { value: 'failed', label: 'Failed' },
      { value: 'Deleting', label: 'Deleting' },
    ],
  },
]

export interface PackageFilterValues {
  type: string
  visibility: string
  status: string
}

export const EMPTY_PACKAGE_FILTERS: PackageFilterValues = {
  type: '',
  visibility: '',
  status: '',
}

export type PackagePaginationMode = 'server' | 'client'

export interface PackagePaginationState {
  mode: PackagePaginationMode
  page: number
  pageSize: number
  query: string
  filters: PackageFilterValues
  cursor: string | null
}

export interface PackagePaginationChangeResult {
  state: PackagePaginationState
  reload: boolean
  clearRows: boolean
}

export interface PackagePageInfo {
  hasNext: boolean
  nextCursor: string | null
}

export function clonePackageFilters(filters: PackageFilterValues): PackageFilterValues {
  return { type: filters.type, visibility: filters.visibility, status: filters.status }
}

export function hasActivePackageFilters(query: string, filters: PackageFilterValues): boolean {
  return !!query.trim() || Object.values(filters).some(Boolean)
}

/**
 * Apply one controlled ResourceTable transition. Server pages retain their
 * opaque cursor; query/filter/page-size changes reset the cursor boundary.
 * Clearing a client-side query always returns to server page one.
 */
export function applyPackagePaginationChange(
  previous: PackagePaginationState,
  change: ResourceTableChange,
): PackagePaginationChangeResult {
  const filters: PackageFilterValues = {
    type: change.filters.type || '',
    visibility: change.filters.visibility || '',
    status: change.filters.status || '',
  }
  const active = hasActivePackageFilters(change.query, filters)
  const preserveServerPage = !active && previous.mode === 'server' && change.reason === 'page'
  const state: PackagePaginationState = {
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

  if (previous.mode === 'client') return { state, reload: false, clearRows: false }
  return { state, reload: true, clearRows: true }
}

export function packagePageInfo(nextCursor?: string): PackagePageInfo {
  const cursor = nextCursor || null
  return { hasNext: cursor !== null, nextCursor: cursor }
}

export function packageVisibility(value: string | undefined): string {
  return value || 'unknown'
}
