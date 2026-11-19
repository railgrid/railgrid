// CANONICAL SOURCE — edit here and run `make sync-portalkit`.
import type { ResourceRefreshMode } from './page-state'

export type { ResourceRefreshMode } from './page-state'

export interface LatestRefreshController {
  request(mode?: ResourceRefreshMode): Promise<void>
  invalidate(): void
  stop(): void
  isCurrent(requestID: number): boolean
}

export const FAST_REFRESH_MS = 10_000
export const STABLE_REFRESH_MS = 30_000

export interface AdaptiveRefreshTimer {
  cancel(): void
  schedule(): void
  stop(): void
}

/**
 * Schedules one background read at a time so the next delay can be selected
 * from the latest committed resource state. Callers schedule again after a
 * read settles; this avoids interval overlap when an API response is slow.
 */
export function createAdaptiveRefreshTimer(
  read: () => void,
  cadence: () => number = () => STABLE_REFRESH_MS,
): AdaptiveRefreshTimer {
  let timer: ReturnType<typeof setTimeout> | undefined
  let stopped = false

  function cancel() {
    if (timer !== undefined) clearTimeout(timer)
    timer = undefined
    globalThis.document?.removeEventListener?.('visibilitychange', wake)
    globalThis.window?.removeEventListener?.('online', wake)
  }

  function wake() {
    if (stopped || globalThis.document?.hidden) return
    cancel()
    read()
  }

  return {
    schedule() {
      if (stopped) return
      cancel()
      globalThis.document?.addEventListener?.('visibilitychange', wake)
      globalThis.window?.addEventListener?.('online', wake)
      timer = setTimeout(wake, cadence())
    },
    cancel,
    stop() {
      stopped = true
      cancel()
    },
  }
}

/**
 * Serializes timer, manual, and mutation refreshes. An ordinary request made
 * during an active read queues at most one follow-up without fencing the valid
 * active response. Foreground intent wins when requests are coalesced. Only an
 * explicit invalidation rejects an old-context response.
 */
export function createLatestRefreshController(
  task: (requestID: number, mode: ResourceRefreshMode) => Promise<void>,
): LatestRefreshController {
  let generation = 0
  let active = false
  let queuedMode: ResourceRefreshMode | undefined
  let stopped = false
  let activeWaiters: Array<() => void> = []
  let nextWaiters: Array<() => void> = []

  function start(mode: ResourceRefreshMode) {
    const requestID = ++generation
    active = true
    activeWaiters = nextWaiters
    nextWaiters = []

    function finish() {
      active = false
      if (requestID === generation) {
        for (const resolve of activeWaiters) resolve()
      } else {
        // Callers awaiting an invalidated read wait for its replacement.
        nextWaiters.push(...activeWaiters)
      }
      activeWaiters = []
      if (queuedMode && !stopped) {
        const nextMode = queuedMode
        queuedMode = undefined
        start(nextMode)
      }
    }

    // Tasks own user-facing error state. A failed read still releases the queue.
    try {
      void task(requestID, mode).then(finish, finish)
    } catch {
      finish()
    }
  }

  return {
    request(mode: ResourceRefreshMode = 'foreground') {
      if (stopped) return Promise.resolve()
      const complete = new Promise<void>(resolve => nextWaiters.push(resolve))
      if (active) {
        if (queuedMode !== 'foreground') queuedMode = mode
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
      for (const resolve of activeWaiters) resolve()
      for (const resolve of nextWaiters) resolve()
      activeWaiters = []
      nextWaiters = []
    },
    isCurrent(requestID) {
      return !stopped && requestID === generation
    },
  }
}

/**
 * A Kubernetes continuation token is pinned to the original list snapshot.
 * Use it once for navigation. To refresh page N, follow a fresh chain up to N,
 * stopping at the last available page if the collection shrank.
 */
export function createResourcePageReader() {
  let navigationCursor: string | null = null
  let lastPage = 1
  let lastReadAt = -Infinity
  return {
    navigate(cursor: string | null) {
      navigationCursor = cursor
    },
    async read<T extends { continue?: string | null }>(
      request: { page: number; pageSize: number; cursor: string | null },
      list: (options: { limit: number; continue?: string }, page: number) => Promise<T>,
      isCurrent: () => boolean,
      mode: ResourceRefreshMode = 'foreground',
    ) {
      // A fresh walk costs up to one LIST per page. Space out deep walks
      // by 30 seconds per three pages, including reads triggered by tab wake.
      const pagesPerInterval = 3
      const now = Date.now()
      if (mode === 'background' && request.page > pagesPerInterval &&
          now - lastReadAt < Math.ceil(request.page / pagesPerInterval) * STABLE_REFRESH_MS) return null
      if (!isCurrent()) return null
      lastReadAt = now
      const navigating = request.page > lastPage && !!request.cursor && request.cursor === navigationCursor
      navigationCursor = null
      let page = navigating ? request.page : 1
      let cursor = navigating ? request.cursor : null
      const seen = new Set<string>()
      while (isCurrent()) {
        const result = await list(cursor ? { limit: request.pageSize, continue: cursor } : { limit: request.pageSize }, page)
        if (!isCurrent()) return null
        if (page >= request.page || !result.continue) {
          lastPage = page
          return { result, page, cursor }
        }
        if (seen.has(result.continue)) throw new Error('The server returned a repeated page cursor.')
        seen.add(result.continue)
        cursor = result.continue
        page++
      }
      return null
    },
  }
}
