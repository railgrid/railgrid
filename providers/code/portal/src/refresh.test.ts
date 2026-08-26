import { describe, expect, it, vi } from 'vitest'

import { createAdaptiveRefreshTimer, createLatestRefreshController, createResourceDeletions, FAST_REFRESH_MS, STABLE_REFRESH_MS, type ResourceRefreshMode } from './refresh'

interface Deferred<T> {
  promise: Promise<T>
  resolve(value: T): void
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(resolvePromise => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

async function settle(): Promise<void> {
  await Promise.resolve()
  await Promise.resolve()
}

describe('resource deletions', () => {
  it('retains an acknowledged UID through stale and terminating snapshots until absence', () => {
    const deletions = createResourceDeletions()

    deletions.acknowledge('connection', 'demo', 'old-uid')
    deletions.reconcile('connection', [{ name: 'demo', uid: 'old-uid' }])
    expect(deletions.has('connection', 'demo', 'old-uid')).toBe(true)

    deletions.reconcile('connection', [])
    expect(deletions.has('connection', 'demo', 'old-uid')).toBe(false)
  })

  it('releases an old acknowledgement when the same name has a new UID', () => {
    const deletions = createResourceDeletions()

    deletions.acknowledge('repository', 'demo', 'old-uid')
    deletions.reconcile('repository', [{ name: 'demo', uid: 'new-uid' }])

    expect(deletions.has('repository', 'demo', 'new-uid')).toBe(false)
  })

  it('reconciles repository-scoped child lists independently', () => {
    const deletions = createResourceDeletions()

    deletions.acknowledge('deploy-key:repo-a', 'key-a', 'uid-a')
    deletions.acknowledge('deploy-key:repo-b', 'key-b', 'uid-b')
    deletions.reconcile('deploy-key:repo-b', [])

    expect(deletions.has('deploy-key:repo-a', 'key-a', 'uid-a')).toBe(true)
    expect(deletions.has('deploy-key:repo-b', 'key-b', 'uid-b')).toBe(false)
  })

  it('clears all acknowledged deletions at an authority boundary', () => {
    const deletions = createResourceDeletions()

    deletions.acknowledge('connection', 'demo', 'uid')
    deletions.clear()

    expect(deletions.has('connection', 'demo', 'uid')).toBe(false)
  })
})

describe('latest refresh controller', () => {
  it('lets the active read commit while coalescing a timer follow-up', async () => {
    const first = deferred<void>()
    const second = deferred<void>()
    const modes: ResourceRefreshMode[] = []
    const commits: number[] = []
    const controller = createLatestRefreshController(async (requestID, mode) => {
      modes.push(mode)
      await (modes.length === 1 ? first.promise : second.promise)
      if (controller.isCurrent(requestID)) commits.push(requestID)
    })

    controller.request('foreground')
    controller.request('background')
    await settle()
    expect(modes).toEqual(['foreground'])
    expect(controller.isCurrent(1)).toBe(true)

    first.resolve()
    await settle()
    expect(commits).toEqual([1])
    expect(modes).toEqual(['foreground', 'background'])

    second.resolve()
    await settle()
  })

  it('promotes a queued foreground request over background work', async () => {
    const first = deferred<void>()
    const second = deferred<void>()
    const modes: ResourceRefreshMode[] = []
    const controller = createLatestRefreshController(async (_requestID, mode) => {
      modes.push(mode)
      await (modes.length === 1 ? first.promise : second.promise)
    })

    controller.request('background')
    controller.request('background')
    controller.request('foreground')
    first.resolve()
    await settle()

    expect(modes).toEqual(['background', 'foreground'])
    second.resolve()
    await settle()
  })

  it('fences an invalidated active read and queues a foreground replacement', async () => {
    const first = deferred<void>()
    const second = deferred<void>()
    const ids: number[] = []
    const modes: ResourceRefreshMode[] = []
    const controller = createLatestRefreshController(async (requestID, mode) => {
      ids.push(requestID)
      modes.push(mode)
      await (ids.length === 1 ? first.promise : second.promise)
    })

    controller.request('background')
    controller.invalidate()
    expect(controller.isCurrent(1)).toBe(false)
    first.resolve()
    await settle()

    expect(ids).toEqual([1, 3])
    expect(modes).toEqual(['background', 'foreground'])
    second.resolve()
    await settle()
  })
})

describe('adaptive refresh timer', () => {
  it('uses the current cadence and cancels a replaced schedule', () => {
    vi.useFakeTimers()
    try {
      const read = vi.fn()
      let cadence = FAST_REFRESH_MS
      const timer = createAdaptiveRefreshTimer(read, () => cadence)

      timer.schedule()
      cadence = STABLE_REFRESH_MS
      timer.schedule()
      vi.advanceTimersByTime(FAST_REFRESH_MS)
      expect(read).not.toHaveBeenCalled()
      vi.advanceTimersByTime(STABLE_REFRESH_MS - FAST_REFRESH_MS)
      expect(read).toHaveBeenCalledTimes(1)
      timer.stop()
      vi.advanceTimersByTime(STABLE_REFRESH_MS)
      expect(read).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
  })
})
