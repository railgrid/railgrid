import { describe, expect, it } from 'vitest'
import {
  EMPTY_REPOSITORY_FILTERS,
  applyRepositoryPaginationChange,
  hasActiveRepositoryFilters,
  repositoryFilters,
  repositoryPageInfo,
  repositoryStatus,
  REPOSITORY_STATUS_OPTIONS,
  REPOSITORY_VISIBILITY_OPTIONS,
} from './repositoriesPagination'

describe('repository table pagination state', () => {
  const emptyChange = {
    pageSize: 10,
    query: '',
    filters: { connectionRef: '', visibility: '', status: '' },
    cursor: null,
  } as const

  it('treats whitespace as inactive and any selected value as active', () => {
    expect(hasActiveRepositoryFilters('  ', { ...EMPTY_REPOSITORY_FILTERS })).toBe(false)
    expect(hasActiveRepositoryFilters('  service  ', { ...EMPTY_REPOSITORY_FILTERS })).toBe(true)
    expect(hasActiveRepositoryFilters('', { ...EMPTY_REPOSITORY_FILTERS, connectionRef: 'github' })).toBe(true)
  })

  it('normalizes opaque page cursors without inventing a total', () => {
    expect(repositoryPageInfo()).toEqual({ hasNext: false, nextCursor: null })
    expect(repositoryPageInfo('opaque-next')).toEqual({ hasNext: true, nextCursor: 'opaque-next' })
    expect(repositoryPageInfo('')).toEqual({ hasNext: false, nextCursor: null })
  })

  it('preserves server page navigation instead of treating it as a clear', () => {
    const transition = applyRepositoryPaginationChange(
      {
        mode: 'server',
        page: 1,
        pageSize: 10,
        query: '',
        filters: { ...EMPTY_REPOSITORY_FILTERS },
        cursor: null,
      },
      { ...emptyChange, reason: 'page', page: 2, cursor: 'opaque-next' },
    )

    expect(transition).toMatchObject({
      reload: true,
      state: { mode: 'server', page: 2, pageSize: 10, cursor: 'opaque-next' },
    })
  })

  it.each([
    ['previous page', { ...emptyChange, reason: 'page' as const, page: 1, cursor: null }, 1, 10, null],
    ['page-size reset', { ...emptyChange, reason: 'page-size' as const, page: 1, pageSize: 25, cursor: null }, 1, 25, null],
  ])('%s keeps the server transition authoritative', (_label, change, page, pageSize, cursor) => {
    const transition = applyRepositoryPaginationChange(
      {
        mode: 'server',
        page: 2,
        pageSize: 10,
        query: '',
        filters: { ...EMPTY_REPOSITORY_FILTERS },
        cursor: 'opaque-page-2',
      },
      change,
    )

    expect(transition).toMatchObject({
      reload: true,
      state: { mode: 'server', page, pageSize, cursor },
    })
  })

  it('enters client mode for an active query and preserves the query state', () => {
    const transition = applyRepositoryPaginationChange(
      {
        mode: 'server',
        page: 2,
        pageSize: 10,
        query: '',
        filters: { ...EMPTY_REPOSITORY_FILTERS },
        cursor: 'opaque-page-2',
      },
      { ...emptyChange, reason: 'query', page: 1, query: 'service' },
    )

    expect(transition).toMatchObject({
      reload: true,
      clearRows: true,
      state: { mode: 'server', page: 1, query: 'service', cursor: null },
    })
  })

  it('returns to server page one when a client query/filter is cleared', () => {
    const transition = applyRepositoryPaginationChange(
      {
        mode: 'client',
        page: 3,
        pageSize: 25,
        query: 'service',
        filters: { ...EMPTY_REPOSITORY_FILTERS, visibility: 'private' },
        cursor: null,
      },
      { ...emptyChange, reason: 'filter', page: 1, pageSize: 25 },
    )

    expect(transition).toMatchObject({
      reload: true,
      state: { mode: 'server', page: 1, pageSize: 25, cursor: null, query: '' },
    })
  })

  it('keeps connection options complete and visibility/status options explicit', () => {
    const filters = repositoryFilters([
      { name: 'zeta', owner: '', provider: '', type: '', secretName: '', scopes: [], validated: true },
      { name: 'alpha', owner: '', provider: '', type: '', secretName: '', scopes: [], validated: true },
      { name: 'alpha', owner: '', provider: '', type: '', secretName: '', scopes: [], validated: true },
    ])
    const byKey = Object.fromEntries(filters.map(filter => [filter.key, filter]))
    expect(byKey.connectionRef.options?.map(option => option.value)).toEqual(['alpha', 'zeta'])
    expect(byKey.connectionRef.control).toBe('combobox')
    expect(byKey.visibility.options).toEqual(REPOSITORY_VISIBILITY_OPTIONS)
    expect(byKey.status.options).toEqual(REPOSITORY_STATUS_OPTIONS)
  })

  it('distinguishes deleting, not-yet-observed, ready, and failed resources', () => {
    expect(repositoryStatus({ deletionTimestamp: 'now', generation: 1, observedGeneration: 1, ready: true })).toBe('Deleting')
    expect(repositoryStatus({ generation: 2, observedGeneration: 1, ready: false, failed: true })).toBe('pending')
    expect(repositoryStatus({ generation: 1, observedGeneration: 1, ready: true })).toBe('ready')
    expect(repositoryStatus({ generation: 1, observedGeneration: 1, ready: false, failed: true })).toBe('failed')
    expect(repositoryStatus({ generation: 1, observedGeneration: 1, ready: false, failed: false })).toBe('pending')
  })
})
