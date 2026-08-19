import { reactive } from 'vue'

export interface LatestRefreshController {
  request(): Promise<void>
  invalidate(): void
  stop(): void
  isCurrent(requestID: number): boolean
}

/**
 * Serializes refreshes and gives only the latest requested read permission to
 * commit. A request made during an active read queues one follow-up read and
 * waits for that latest cycle, which keeps polling and mutation reconciliation
 * from racing each other.
 */
export function createLatestRefreshController(
  task: (requestID: number) => Promise<void>,
): LatestRefreshController {
  let generation = 0
  let active = false
  let queued = false
  let stopped = false
  let waiters: Array<() => void> = []

  const settleWaiters = () => {
    const pending = waiters
    waiters = []
    for (const resolve of pending) resolve()
  }

  const start = () => {
    if (stopped || active) return
    const requestID = ++generation
    active = true
    void task(requestID).catch(() => {
      // The task owns user-visible error state.
    }).finally(() => {
      active = false
      if (queued && !stopped) {
        queued = false
        start()
      } else {
        settleWaiters()
      }
    })
  }

  return {
    request() {
      if (stopped) return Promise.resolve()
      const complete = new Promise<void>(resolve => waiters.push(resolve))
      if (active) {
        generation += 1
        queued = true
      } else {
        start()
      }
      return complete
    },
    invalidate() {
      if (stopped) return
      generation += 1
      if (active) queued = true
    },
    stop() {
      stopped = true
      generation += 1
      queued = false
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
