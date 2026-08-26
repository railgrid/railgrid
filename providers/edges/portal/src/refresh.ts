import type { ResourceRefreshMode } from './portalkit/page-state'

export type { ResourceRefreshMode } from './portalkit/page-state'

export const FAST_REFRESH_MS = 10_000
export const STABLE_REFRESH_MS = 30_000

export interface LatestRefreshController {
  request(mode?: ResourceRefreshMode): Promise<void>
  invalidate(): void
  stop(): void
  isCurrent(requestID: number): boolean
}

/**
 * Serialize refreshes without letting a timer invalidate a useful active read.
 * One follow-up is retained; a foreground request always outranks a queued
 * background request. Identity changes still fence the old response through
 * invalidate().
 */
export function createLatestRefreshController(
  task: (requestID: number, mode: ResourceRefreshMode) => Promise<void>,
): LatestRefreshController {
  let generation = 0
  let active = false
  let queuedMode: ResourceRefreshMode | undefined
  let stopped = false
  let waiters: Array<() => void> = []

  const settleWaiters = () => {
    const pending = waiters
    waiters = []
    for (const resolve of pending) resolve()
  }

  const start = (mode: ResourceRefreshMode) => {
    if (stopped || active) return
    const requestID = ++generation
    active = true
    void task(requestID, mode).catch(() => {
      // Tasks own user-facing error state.
    }).finally(() => {
      active = false
      if (queuedMode && !stopped) {
        const nextMode = queuedMode
        queuedMode = undefined
        start(nextMode)
      } else {
        settleWaiters()
      }
    })
  }

  return {
    request(mode = 'foreground') {
      if (stopped) return Promise.resolve()
      const complete = new Promise<void>(resolve => waiters.push(resolve))
      if (active) {
        queuedMode = queuedMode === 'foreground' || mode === 'foreground' ? 'foreground' : 'background'
      } else {
        start(mode)
      }
      return complete
    },
    invalidate() {
      if (stopped) return
      generation += 1
      if (active) queuedMode = 'foreground'
    },
    stop() {
      stopped = true
      generation += 1
      queuedMode = undefined
      settleWaiters()
    },
    isCurrent(requestID) {
      return !stopped && requestID === generation
    },
  }
}

export interface AdaptiveRefreshTimer {
  schedule(): void
  stop(): void
}

/** A one-shot poller whose next delay is derived from the latest snapshot. */
export function createAdaptiveRefreshTimer(
  read: () => void,
  cadence: () => number,
): AdaptiveRefreshTimer {
  let timer: ReturnType<typeof setTimeout> | undefined
  let stopped = false

  return {
    schedule() {
      if (stopped) return
      if (timer !== undefined) clearTimeout(timer)
      timer = setTimeout(() => {
        timer = undefined
        if (!stopped) read()
      }, cadence())
    },
    stop() {
      stopped = true
      if (timer !== undefined) clearTimeout(timer)
      timer = undefined
    },
  }
}
