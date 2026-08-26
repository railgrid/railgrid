import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  createAdaptiveRefreshTimer,
  createLatestRefreshController,
  createResourceTombstones,
  FAST_REFRESH_MS,
  sameResourceIdentity,
  STABLE_REFRESH_MS,
} from './refresh'

describe('latest refresh controller', () => {
  it('lets an active snapshot commit before one queued background read', async () => {
    let releaseFirst!: () => void
    let calls = 0
    const modes: string[] = []
    const committed: string[] = []
    const refresh = createLatestRefreshController(async (requestID, mode) => {
      modes.push(mode)
      if (calls++ === 0) await new Promise<void>(resolve => { releaseFirst = resolve })
      if (refresh.isCurrent(requestID)) committed.push(mode)
    })

    const first = refresh.request('foreground')
    const second = refresh.request('background')
    releaseFirst()
    await Promise.all([first, second])

    expect(modes).toEqual(['foreground', 'background'])
    expect(committed).toEqual(['foreground', 'background'])
    refresh.stop()
  })

  it('coalesces queued reads and lets foreground intent dominate background', async () => {
    let releaseFirst!: () => void
    const modes: string[] = []
    const refresh = createLatestRefreshController(async (_requestID, mode) => {
      modes.push(mode)
      if (modes.length === 1) await new Promise<void>(resolve => { releaseFirst = resolve })
    })

    const first = refresh.request('background')
    const second = refresh.request('background')
    const third = refresh.request('foreground')
    releaseFirst()
    await Promise.all([first, second, third])

    expect(modes).toEqual(['background', 'foreground'])
    refresh.stop()
  })

  it('fences an invalidated context and runs one foreground replacement', async () => {
    let releaseFirst!: () => void
    const committed: string[] = []
    const modes: string[] = []
    const refresh = createLatestRefreshController(async (requestID, mode) => {
      modes.push(mode)
      if (modes.length === 1) await new Promise<void>(resolve => { releaseFirst = resolve })
      if (refresh.isCurrent(requestID)) committed.push(mode)
    })

    const completion = refresh.request('background')
    refresh.invalidate()
    releaseFirst()
    await completion

    expect(modes).toEqual(['background', 'foreground'])
    expect(committed).toEqual(['foreground'])
    refresh.stop()
  })
})

describe('adaptive refresh timer', () => {
  afterEach(() => vi.useRealTimers())

  it('uses the current fast or stable cadence and stops future reads', () => {
    vi.useFakeTimers()
    let delay = FAST_REFRESH_MS
    const read = vi.fn()
    const timer = createAdaptiveRefreshTimer(read, () => delay)

    timer.schedule()
    vi.advanceTimersByTime(FAST_REFRESH_MS - 1)
    expect(read).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    expect(read).toHaveBeenCalledTimes(1)

    delay = STABLE_REFRESH_MS
    timer.schedule()
    vi.advanceTimersByTime(STABLE_REFRESH_MS)
    expect(read).toHaveBeenCalledTimes(2)

    timer.schedule()
    timer.stop()
    vi.advanceTimersByTime(STABLE_REFRESH_MS)
    expect(read).toHaveBeenCalledTimes(2)
  })
})

describe('resource tombstones', () => {
  it('keeps a Back or direct-route stale same-UID read marked until list absence', () => {
    const tombstones = createResourceTombstones()

    tombstones.add('demo', 'old-uid')
    expect(tombstones.has('demo', 'old-uid')).toBe(true)

    // A newly mounted detail/list route sees the same shared marker, so a
    // stale read cannot repaint the acknowledged UID as active.
    tombstones.reconcile([{ name: 'demo', uid: 'old-uid' }])
    expect(tombstones.has('demo', 'old-uid')).toBe(true)

    tombstones.reconcile([])
    expect(tombstones.has('demo', 'old-uid')).toBe(false)
  })

  it('retains a tombstone through termination and stale snapshots until true absence', () => {
    const tombstones = createResourceTombstones()

    tombstones.add('demo', 'old-uid')

    // listInstances renders this terminating resource as Deleting and returns
    // its raw identity for marker reconciliation.
    tombstones.reconcile([{ name: 'demo', uid: 'old-uid' }])
    expect(tombstones.has('demo', 'old-uid')).toBe(true)

    // An older list snapshot that still presents the object as active must not
    // resurrect the acknowledged deletion.
    tombstones.reconcile([{ name: 'demo', uid: 'old-uid' }])
    expect(tombstones.has('demo', 'old-uid')).toBe(true)

    tombstones.reconcile([])
    expect(tombstones.has('demo', 'old-uid')).toBe(false)

    tombstones.reconcile([{ name: 'demo', uid: 'new-uid' }])
    expect(tombstones.has('demo', 'new-uid')).toBe(false)
  })

  it('reveals a same-name replacement with a different UID', () => {
    const tombstones = createResourceTombstones()
    tombstones.add('demo', 'old-uid')

    tombstones.reconcile([{ name: 'demo', uid: 'new-uid' }])

    expect(tombstones.has('demo', 'new-uid')).toBe(false)
  })

  it('keeps an unknown-UID deletion marked until the name is absent', () => {
    const tombstones = createResourceTombstones()
    tombstones.add('demo')

    // Without a UID the current row cannot prove whether it is the object
    // acknowledged for deletion or a same-name replacement.
    tombstones.reconcile([{ name: 'demo', uid: 'new-uid' }])
    expect(tombstones.has('demo', 'new-uid')).toBe(true)

    tombstones.reconcile([])
    expect(tombstones.has('demo', 'new-uid')).toBe(false)
  })

  it('clears acknowledged deletions when authority changes', () => {
    const tombstones = createResourceTombstones()
    tombstones.add('demo', 'old-uid')

    tombstones.clear()

    expect(tombstones.has('demo', 'old-uid')).toBe(false)
  })
})

describe('resource identity revalidation', () => {
  it('rejects a same-name replacement that appears while confirmation is open', () => {
    const expected = { name: 'demo', uid: 'old-uid' }

    expect(sameResourceIdentity(expected, { name: 'demo', uid: 'new-uid' })).toBe(false)
    expect(sameResourceIdentity(expected, { name: 'demo', uid: 'old-uid' })).toBe(true)
  })

  it('requires the same object reference when the API omitted UID', () => {
    const expected = { name: 'demo' }

    expect(sameResourceIdentity(expected, expected)).toBe(true)
    expect(sameResourceIdentity(expected, { name: 'demo' })).toBe(false)
  })
})
