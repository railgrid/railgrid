import { describe, expect, it, vi } from 'vitest'
import { createFullListReadCoordinator } from './hybridPagination'

describe('full-list read coordinator', () => {
  it('coalesces rapid callers into one bounded walk and retains its result', async () => {
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

    resolveRead(['alpha', 'beta'])
    await expect(first).resolves.toEqual(['alpha', 'beta'])
    await expect(coordinator.read()).resolves.toEqual(['alpha', 'beta'])
    expect(readFullList).toHaveBeenCalledTimes(1)
  })

  it('updates the cache only after a successful complete walk', async () => {
    let rejectRead!: (error: Error) => void
    const readFullList = vi.fn<() => Promise<readonly string[]>>()
      .mockImplementationOnce(() => new Promise<readonly string[]>((_resolve, reject) => {
        rejectRead = reject
      }))
      .mockRejectedValue(new Error('bounded walk failed'))
    const coordinator = createFullListReadCoordinator(readFullList)

    const failed = coordinator.read()
    await Promise.resolve()
    rejectRead(new Error('bounded walk failed'))
    await expect(failed).rejects.toThrow('bounded walk failed')
    expect(coordinator.peek()).toBeNull()

    await expect(coordinator.read()).rejects.toThrow('bounded walk failed')
    expect(readFullList).toHaveBeenCalledTimes(2)
  })

  it('seeds a complete first page and refreshes it only when forced', async () => {
    const readFullList = vi.fn(async () => ['fresh'])
    const coordinator = createFullListReadCoordinator(readFullList)
    expect(coordinator.seed(['first'])).toEqual(['first'])

    await expect(coordinator.read()).resolves.toEqual(['first'])
    expect(readFullList).not.toHaveBeenCalled()
    await expect(coordinator.read(true)).resolves.toEqual(['fresh'])
    expect(readFullList).toHaveBeenCalledTimes(1)
    coordinator.clear()
    expect(coordinator.peek()).toBeNull()
    await expect(coordinator.read()).resolves.toEqual(['fresh'])
    expect(readFullList).toHaveBeenCalledTimes(2)
  })

  it('does not let a cleared in-flight walk repopulate the next authority', async () => {
    let resolveOld!: (items: readonly string[]) => void
    const readFullList = vi.fn<() => Promise<readonly string[]>>()
      .mockImplementationOnce(() => new Promise<readonly string[]>(resolve => {
        resolveOld = resolve
      }))
      .mockResolvedValue(['new'])
    const coordinator = createFullListReadCoordinator(readFullList)

    const oldRead = coordinator.read()
    await Promise.resolve()
    coordinator.clear()
    const replacementRead = coordinator.read()
    expect(readFullList).toHaveBeenCalledTimes(1)

    resolveOld(['old'])
    await expect(oldRead).resolves.toEqual(['old'])
    await expect(replacementRead).resolves.toEqual(['new'])
    expect(readFullList).toHaveBeenCalledTimes(2)
    expect(coordinator.peek()).toEqual(['new'])
  })

  it('requires a fresh serialized walk when clear happens before a pending walk resolves', async () => {
    let resolveOld!: (items: readonly string[]) => void
    let activeReads = 0
    let maximumActiveReads = 0
    const readFullList = vi.fn<() => Promise<readonly string[]>>()
      .mockImplementationOnce(() => new Promise<readonly string[]>(resolve => {
        activeReads += 1
        maximumActiveReads = Math.max(maximumActiveReads, activeReads)
        resolveOld = items => {
          activeReads -= 1
          resolve(items)
        }
      }))
      .mockImplementationOnce(async () => {
        activeReads += 1
        maximumActiveReads = Math.max(maximumActiveReads, activeReads)
        activeReads -= 1
        return ['fresh-after-clear']
      })
    const coordinator = createFullListReadCoordinator(readFullList)

    const pending = coordinator.read()
    await Promise.resolve()
    coordinator.clear()
    resolveOld(['stale-before-clear'])
    await expect(pending).resolves.toEqual(['stale-before-clear'])
    expect(coordinator.peek()).toBeNull()

    // This models the later search after the queued server page has settled:
    // it cannot reuse the pre-clear walk and must start a fresh serialized one.
    await expect(coordinator.read()).resolves.toEqual(['fresh-after-clear'])
    expect(readFullList).toHaveBeenCalledTimes(2)
    expect(maximumActiveReads).toBe(1)
  })

  it('does not expose mutable cache storage to stale consumers', () => {
    const coordinator = createFullListReadCoordinator(() => ['one'])
    const seeded = coordinator.seed(['one'])
    seeded.push('stale-local-mutation')
    const cached = coordinator.peek()
    expect(cached).toEqual(['one'])
    cached?.push('another-stale-mutation')
    expect(coordinator.peek()).toEqual(['one'])
  })
})
