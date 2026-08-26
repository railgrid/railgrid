import { describe, expect, it, vi } from 'vitest'

import {
  FAST_REFRESH_MS,
  STABLE_REFRESH_MS,
  createAdaptiveRefreshTimer,
  createLatestRefreshController,
} from './refresh'

function deferred() {
  let resolve!: () => void
  const promise = new Promise<void>(resolvePromise => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

describe('edge refresh coordination', () => {
  it('lets an active snapshot commit and coalesces newer work with foreground priority', async () => {
    const first = deferred()
    const second = deferred()
    const calls: Array<{ id: number; mode: string; currentAfterRead?: boolean }> = []
    const controller = createLatestRefreshController(async (id, mode) => {
      calls.push({ id, mode })
      await (calls.length === 1 ? first.promise : second.promise)
      calls.at(-1)!.currentAfterRead = controller.isCurrent(id)
    })

    const active = controller.request('background')
    const queuedBackground = controller.request('background')
    const queuedForeground = controller.request('foreground')
    expect(calls).toEqual([{ id: 1, mode: 'background' }])

    first.resolve()
    await vi.waitFor(() => expect(calls).toHaveLength(2))
    expect(calls[0]).toMatchObject({ id: 1, mode: 'background', currentAfterRead: true })
    expect(calls[1]).toMatchObject({ id: 2, mode: 'foreground' })

    second.resolve()
    await Promise.all([active, queuedBackground, queuedForeground])
    expect(calls).toHaveLength(2)
  })

  it('fences an invalidated context and runs one foreground replacement', async () => {
    const first = deferred()
    const second = deferred()
    const current: boolean[] = []
    const modes: string[] = []
    const controller = createLatestRefreshController(async (id, mode) => {
      modes.push(mode)
      await (modes.length === 1 ? first.promise : second.promise)
      current.push(controller.isCurrent(id))
    })

    const active = controller.request('background')
    controller.invalidate()
    const replacement = controller.request('background')
    first.resolve()
    await vi.waitFor(() => expect(modes).toHaveLength(2))
    expect(current).toEqual([false])
    expect(modes).toEqual(['background', 'foreground'])
    second.resolve()
    await Promise.all([active, replacement])
    expect(current).toEqual([false, true])
  })

  it('uses a single adaptive timer and replaces its pending cadence', () => {
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
    } finally {
      vi.useRealTimers()
    }
  })
})
