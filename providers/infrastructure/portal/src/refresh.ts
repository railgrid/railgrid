import { reactive } from 'vue'
import type { ResourceRefreshMode } from './portalkit/page-state'

export type { ResourceRefreshMode } from './portalkit/page-state'

export interface LatestRefreshController {
  request(mode?: ResourceRefreshMode): Promise<void>
  invalidate(): void
  stop(): void
  isCurrent(requestID: number): boolean
}

export const FAST_REFRESH_MS = 10_000
export const STABLE_REFRESH_MS = 30_000

export interface AdaptiveRefreshTimer {
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
      // The task owns user-visible error state.
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
    request(mode: ResourceRefreshMode = 'foreground') {
      if (stopped) return Promise.resolve()
      const complete = new Promise<void>(resolve => waiters.push(resolve))
      if (active) {
        queuedMode = queuedMode === 'foreground' || mode === 'foreground'
          ? 'foreground'
          : 'background'
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

/** Authority-local acknowledged deletions remain marked Deleting until a
 * successful list proves the resource is absent. When the API could not
 * provide a UID, a same-name row is still not proof that the deleted object
 * is gone, so retain the marker until the name disappears. The app owner
 * clears this registry when the tenant changes so markers cannot cross KRM
 * authorities. */
export interface ResourceTombstones {
  add(name: string, uid?: string): void
  has(name: string, uid?: string): boolean
  reconcile(resources: readonly { name: string; uid?: string }[]): void
  clear(): void
}

export interface ResourceIdentity {
  name: string
  uid?: string
}

/**
 * Confirms that an object shown before an async user decision is still the
 * object currently rendered afterward. Kubernetes UID is authoritative when
 * available. Without one, only the exact same object reference is safe: a
 * refresh may otherwise have replaced a same-name resource invisibly.
 */
export function sameResourceIdentity<T extends ResourceIdentity>(
  expected: T,
  current: T | null | undefined,
): current is T {
  if (!current || expected.name !== current.name) return false
  if (expected.uid !== undefined || current.uid !== undefined) {
    return expected.uid !== undefined && current.uid !== undefined && expected.uid === current.uid
  }
  return expected === current
}

export function createResourceTombstones(): ResourceTombstones {
  const identities = reactive(new Map<string, string | null>())
  return {
    add(name: string, uid?: string) {
      identities.set(name, uid ?? null)
    },
    has(name: string, uid?: string) {
      if (!identities.has(name)) return false
      const expected = identities.get(name)
      return expected === null || uid === undefined || expected === uid
    },
    reconcile(resources: readonly { name: string; uid?: string }[]) {
      const present = new Map(resources.map(resource => [resource.name, resource.uid]))
      for (const [name, expected] of [...identities]) {
        const current = present.get(name)
        if (!present.has(name) ||
          (expected !== null && current !== undefined && current !== expected)) {
          identities.delete(name)
        }
      }
    },
    clear() {
      identities.clear()
    },
  }
}
