import { describe, expect, it } from 'vitest'
import {
  applyPackagePaginationChange,
  EMPTY_PACKAGE_FILTERS,
  hasActivePackageFilters,
  PACKAGE_FILTERS,
  packagePageInfo,
  packageVisibility,
} from './packagesPagination'

describe('package table pagination state', () => {
  const emptyChange = {
    pageSize: 10,
    query: '',
    filters: { ...EMPTY_PACKAGE_FILTERS },
    cursor: null,
  } as const

  it('treats whitespace as inactive and any selected value as active', () => {
    expect(hasActivePackageFilters('  ', { ...EMPTY_PACKAGE_FILTERS })).toBe(false)
    expect(hasActivePackageFilters('  image  ', { ...EMPTY_PACKAGE_FILTERS })).toBe(true)
    expect(hasActivePackageFilters('', { ...EMPTY_PACKAGE_FILTERS, type: 'container' })).toBe(true)
  })

  it('normalizes page cursors without inventing a total', () => {
    expect(packagePageInfo()).toEqual({ hasNext: false, nextCursor: null })
    expect(packagePageInfo('next-page')).toEqual({ hasNext: true, nextCursor: 'next-page' })
    expect(packagePageInfo('')).toEqual({ hasNext: false, nextCursor: null })
  })

  it('declares the complete GitHub package filter vocabulary', () => {
    const byKey = Object.fromEntries(PACKAGE_FILTERS.map(filter => [filter.key, filter]))
    expect(byKey.type.options?.map(option => option.value)).toEqual([
      'container', 'docker', 'npm', 'maven', 'rubygems', 'nuget',
    ])
    expect(byKey.visibility.options?.map(option => option.value)).toEqual([
      'public', 'internal', 'private', 'unknown',
    ])
    expect(byKey.status.options?.map(option => option.value)).toEqual([
      'ready', 'pending', 'failed', 'Deleting',
    ])
  })

  it('keeps missing host visibility filterable as unknown while display can stay blank', () => {
    expect(packageVisibility(undefined)).toBe('unknown')
    expect(packageVisibility('private')).toBe('private')
  })

  it('preserves an unfiltered server cursor page transition', () => {
    const transition = applyPackagePaginationChange(
      {
        mode: 'server',
        page: 1,
        pageSize: 10,
        query: '',
        filters: { ...EMPTY_PACKAGE_FILTERS },
        cursor: null,
      },
      { ...emptyChange, reason: 'page', page: 2, cursor: 'opaque-next' },
    )

    expect(transition).toMatchObject({
      reload: true,
      state: { mode: 'server', page: 2, cursor: 'opaque-next' },
    })
  })

  it('resets a client-side clear to server page one', () => {
    const transition = applyPackagePaginationChange(
      {
        mode: 'client',
        page: 3,
        pageSize: 25,
        query: 'image',
        filters: { ...EMPTY_PACKAGE_FILTERS, type: 'container' },
        cursor: null,
      },
      { ...emptyChange, reason: 'filter', page: 1, pageSize: 25 },
    )

    expect(transition).toMatchObject({
      reload: true,
      clearRows: true,
      state: { mode: 'server', page: 1, pageSize: 25, cursor: null, query: '' },
    })
  })

  it('keeps active client changes local without starting another read', () => {
    const transition = applyPackagePaginationChange(
      {
        mode: 'client',
        page: 1,
        pageSize: 10,
        query: 'image',
        filters: { ...EMPTY_PACKAGE_FILTERS },
        cursor: null,
      },
      { ...emptyChange, reason: 'query', page: 1, query: 'container' },
    )

    expect(transition).toMatchObject({
      reload: false,
      clearRows: false,
      state: { mode: 'client', query: 'container' },
    })
  })
})
