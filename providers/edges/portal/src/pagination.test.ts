import { describe, expect, it, vi } from 'vitest'

import {
  createFullListReadCoordinator,
  createInFlightReadCoordinator,
  hasActiveTableFilters,
  sameTableRequest,
  tablePageInfo,
  type TableRequestState,
} from './pagination'

describe('edges table pagination state', () => {
  it('treats whitespace as inactive and any selected filter as active', () => {
    expect(hasActiveTableFilters('  ', { edgeName: '', status: '' })).toBe(false)
    expect(hasActiveTableFilters(' nginx ', { edgeName: '', status: '' })).toBe(true)
    expect(hasActiveTableFilters('', { edgeName: 'edge-a', status: '' })).toBe(true)
  })

  it('normalizes empty continuation metadata without claiming a total', () => {
    expect(tablePageInfo()).toEqual({ hasNext: false, nextCursor: null })
    expect(tablePageInfo('')).toEqual({ hasNext: false, nextCursor: null })
    expect(tablePageInfo('opaque-next')).toEqual({ hasNext: true, nextCursor: 'opaque-next' })
  })

  it('rejects stale reads when any request dimension changed', () => {
    const request: TableRequestState = {
      mode: 'server', active: false, page: 1, pageSize: 10,
      query: '', filters: { edgeName: '', status: '' }, cursor: null,
    }
    expect(sameTableRequest(request, { ...request, filters: { ...request.filters } })).toBe(true)
    expect(sameTableRequest(request, { ...request, cursor: 'next' })).toBe(false)
    expect(sameTableRequest(request, { ...request, filters: { edgeName: 'edge-a', status: '' } })).toBe(false)
    expect(sameTableRequest(request, { ...request, mode: 'client', active: true })).toBe(false)
  })

  it('coalesces rapid complete reads and retains only successful query-independent data', async () => {
    let resolveRead!: (items: readonly string[]) => void
    const readFullList = vi.fn(() => new Promise<readonly string[]>(resolve => {
      resolveRead = resolve
    }))
    const coordinator = createFullListReadCoordinator(readFullList)

    const first = coordinator.read()
    const second = coordinator.read()
    expect(second).toBe(first)
    await Promise.resolve()
    expect(readFullList).toHaveBeenCalledTimes(1)
    expect(coordinator.peek()).toBeNull()
    expect(coordinator.pending()).toBe(true)

    resolveRead(['one', 'two'])
    await expect(first).resolves.toEqual(['one', 'two'])
    await expect(coordinator.read()).resolves.toEqual(['one', 'two'])
    expect(readFullList).toHaveBeenCalledTimes(1)
    expect(coordinator.pending()).toBe(false)
  })

  it('queues one forced refresh after a pending walk and invalidates cleared in-flight data', async () => {
    let resolveFirst!: (items: readonly string[]) => void
    let resolveSecond!: (items: readonly string[]) => void
    let resolveThird!: (items: readonly string[]) => void
    const readFullList = vi.fn()
      .mockImplementationOnce(() => new Promise<readonly string[]>(resolve => { resolveFirst = resolve }))
      .mockImplementationOnce(() => new Promise<readonly string[]>(resolve => { resolveSecond = resolve }))
      .mockImplementationOnce(() => new Promise<readonly string[]>(resolve => { resolveThird = resolve }))
    const coordinator = createFullListReadCoordinator(readFullList)

    const initial = coordinator.read()
    const forced = coordinator.read(true)
    await Promise.resolve()
    expect(readFullList).toHaveBeenCalledTimes(1)
    expect(coordinator.pending()).toBe(true)
    resolveFirst(['stale'])
    await expect(initial).resolves.toEqual(['stale'])
    expect(readFullList).toHaveBeenCalledTimes(2)

    resolveSecond(['fresh'])
    await expect(forced).resolves.toEqual(['fresh'])
    expect(coordinator.peek()).toEqual(['fresh'])
    expect(coordinator.pending()).toBe(false)

    const pending = coordinator.read(true)
    await Promise.resolve()
    // The forced read above is settled, so this starts a new walk.
    expect(readFullList).toHaveBeenCalledTimes(3)
    // A clear invalidates the result even if its in-flight request succeeds.
    coordinator.clear()
    resolveThird(['after-clear'])
    await expect(pending).resolves.toEqual(['after-clear'])
    expect(coordinator.peek()).toBeNull()
  })

  it('serializes a fresh read after clear invalidates an in-flight walk', async () => {
    let resolveStale!: (items: readonly string[]) => void
    let resolveFresh!: (items: readonly string[]) => void
    const readFullList = vi.fn()
      .mockImplementationOnce(() => new Promise<readonly string[]>(resolve => { resolveStale = resolve }))
      .mockImplementationOnce(() => new Promise<readonly string[]>(resolve => { resolveFresh = resolve }))
    const coordinator = createFullListReadCoordinator(readFullList)

    const stale = coordinator.read()
    await Promise.resolve()
    expect(readFullList).toHaveBeenCalledTimes(1)

    coordinator.clear()
    const fresh = coordinator.read()
    expect(fresh).not.toBe(stale)
    expect(readFullList).toHaveBeenCalledTimes(1)

    resolveStale(['stale'])
    await expect(stale).resolves.toEqual(['stale'])
    await Promise.resolve()
    await Promise.resolve()
    expect(readFullList).toHaveBeenCalledTimes(2)
    expect(coordinator.peek()).toBeNull()

    resolveFresh(['fresh'])
    await expect(fresh).resolves.toEqual(['fresh'])
    expect(coordinator.peek()).toEqual(['fresh'])
  })

  it('shares only the in-flight supporting read and starts fresh after it settles', async () => {
    let resolveFirst!: (value: string) => void
    let resolveSecond!: (value: string) => void
    const readValue = vi.fn()
      .mockImplementationOnce(() => new Promise<string>(resolve => { resolveFirst = resolve }))
      .mockImplementationOnce(() => new Promise<string>(resolve => { resolveSecond = resolve }))
    const coordinator = createInFlightReadCoordinator(readValue)

    const first = coordinator.read()
    const joined = coordinator.read()
    expect(joined).toBe(first)
    expect(coordinator.pending()).toBe(true)
    await Promise.resolve()
    expect(readValue).toHaveBeenCalledTimes(1)

    resolveFirst('first')
    await expect(first).resolves.toBe('first')
    expect(coordinator.pending()).toBe(false)

    const next = coordinator.read()
    expect(next).not.toBe(first)
    await Promise.resolve()
    expect(readValue).toHaveBeenCalledTimes(2)
    resolveSecond('second')
    await expect(next).resolves.toBe('second')
  })
})
