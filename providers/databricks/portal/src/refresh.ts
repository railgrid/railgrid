import { reactive } from 'vue'

// Serializes timer/manual/mutation refreshes. A request made while one is in
// flight queues one latest read without fencing the active read. This lets a
// timer observe the current snapshot instead of starving it, while a queued
// foreground request still wins over any queued background request.
export interface LatestRefreshController {
  request(mode?: ResourceRefreshMode): void
  invalidate(): void
  stop(): void
  isCurrent(requestID: number): boolean
}

/** A read's presentation priority, independent of whether it is in flight. */
export type ResourceRefreshMode = 'foreground' | 'background'

export const FAST_REFRESH_MS = 5_000
export const STABLE_REFRESH_MS = 30_000

export interface AdaptiveRefreshTimer {
  schedule(): void
  stop(): void
}

/**
 * Schedule one background read at a time. A one-shot timer lets callers adapt
 * the next cadence from the latest resource snapshot without accumulating
 * intervals while a slow read is in flight.
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
 * Share one bounded read between callers that arrive while it is in flight.
 * The read itself remains serialized: a subsequent caller starts only after
 * the previous promise has settled. This is useful for query-independent
 * complete-list walks, where replacing the query must not discard a result
 * that is still valid for the newest local filter.
 */
export interface CoalescedRead<T> {
  request(): Promise<T>
  /** Ignore an in-flight result for the next caller, while staying serialized. */
  invalidate(): void
  stop(): void
}

export function createCoalescedRead<T>(read: () => Promise<T>): CoalescedRead<T> {
  let pending: Promise<T> | undefined
  let queued: Promise<T> | undefined
  let invalidated = false
  let stopped = false

  function start(): Promise<T> {
    if (stopped) return Promise.reject(new Error('read controller is stopped'))
    let current: Promise<T>
    try {
      current = Promise.resolve(read())
    } catch (error) {
      current = Promise.reject(error)
    }
    pending = current
    void current.then(
      () => {
        if (pending === current && !invalidated) pending = undefined
      },
      () => {
        if (pending === current && !invalidated) pending = undefined
      },
    )
    return current
  }

  return {
    request() {
      if (stopped) return Promise.reject(new Error('read controller is stopped'))
      if (!pending) {
        invalidated = false
        return start()
      }
      if (!invalidated) return pending
      if (queued) return queued
      queued = pending.then(
        () => {
          queued = undefined
          invalidated = false
          return start()
        },
        () => {
          queued = undefined
          invalidated = false
          return start()
        },
      )
      return queued
    },
    invalidate() {
      if (pending) invalidated = true
    },
    stop() {
      stopped = true
    },
  }
}

/**
 * Serializes mutations by resource identity rather than by page. A save and
 * delete for the same object share one key, while unrelated rows can proceed
 * independently. The boolean return makes duplicate clicks a no-op at the
 * call site before a second network request is created.
 */
export interface OperationLocks {
  acquire(key: string, phase?: OperationPhase): boolean
  release(key: string): void
  isLocked(key: string): boolean
  phase(key: string): OperationPhase | undefined
  /** Hide an acknowledged deletion until a later authoritative list omits it. */
  tombstone(key: string, uid?: string): void
  isTombstoned(key: string, uid?: string): boolean
  /** Reconcile tombstones against a successful authoritative list read. */
  reconcile(kind: string, resources: readonly (ResourceIdentity | string)[]): void
}

export interface ResourceIdentity {
  name: string
  uid?: string
}

export type OperationPhase = 'creating' | 'saving' | 'deleting'

interface OperationDomain {
  locked: Map<string, OperationPhase>
  /** The UID acknowledged by delete, or null when the server omitted it. */
  tombstones: Map<string, string | null>
}

function newOperationDomain(): OperationDomain {
  return {
    locked: reactive(new Map<string, OperationPhase>()),
    tombstones: reactive(new Map<string, string | null>()),
  }
}

let sharedContext = 'default'
let currentDomain = newOperationDomain()

/**
 * Mutation ownership and deletion tombstones belong to the selected tenant,
 * not to an individual route component. A context switch starts a clean
 * ownership domain so a delayed old-route finally block cannot unlock or hide
 * a resource in the new workspace.
 */
export function setOperationContext(context: string | null | undefined): void {
  const next = context || 'default'
  if (next === sharedContext) return
  sharedContext = next
  // Keep the old domain alive for any delayed callbacks, but never let those
  // callbacks release a lock or tombstone in the newly selected context.
  currentDomain = newOperationDomain()
}

export function createOperationLocks(): OperationLocks {
  const domain = currentDomain
  return {
    acquire(key, phase = 'saving') {
      if (domain.locked.has(key)) return false
      domain.locked.set(key, phase)
      return true
    },
    release(key) {
      domain.locked.delete(key)
    },
    isLocked(key) {
      return domain.locked.has(key)
    },
    phase(key) {
      return domain.locked.get(key)
    },
    tombstone(key, uid) {
      domain.tombstones.set(key, uid ?? null)
    },
    isTombstoned(key, uid) {
      if (!domain.tombstones.has(key)) return false
      const expectedUID = domain.tombstones.get(key)
      // A missing tombstone UID cannot identify a returned resource. Keep it
      // hidden only while the row also lacks an identity; an authoritative UID
      // must be rendered and reconciled rather than hidden forever.
      return expectedUID === null ? uid === undefined : uid === undefined || expectedUID === uid
    },
    reconcile(kind, resources) {
      const present = new Map(resources.map(resource => typeof resource === 'string' ? [resource, undefined] : [resource.name, resource.uid]))
      const prefix = `${kind}:`
      for (const [key, expectedUID] of [...domain.tombstones]) {
        if (!key.startsWith(prefix)) continue
        const name = key.slice(prefix.length)
        const currentUID = present.get(name)
        if (!present.has(name) ||
          (expectedUID !== null && currentUID !== undefined && currentUID !== expectedUID) ||
          (expectedUID === null && currentUID !== undefined)) {
          domain.tombstones.delete(key)
        }
      }
    },
  }
}

export function operationKey(kind: string, name: string): string {
  return `${kind}:${name}`
}

export function createLatestRefreshController(
  task: (requestID: number, mode: ResourceRefreshMode) => Promise<void>,
): LatestRefreshController {
  let generation = 0
  let active = false
  let queuedMode: ResourceRefreshMode | undefined
  let stopped = false

  const request = (mode: ResourceRefreshMode = 'foreground') => {
    if (stopped) return
    if (active) {
      // A foreground request supersedes an active background read (and keeps
      // the historical foreground-over-foreground latest-result behavior).
      // Background polling never fences the active result; it only asks for a
      // serialized follow-up after that result commits.
      if (mode === 'foreground') generation += 1
      queuedMode = queuedMode === 'foreground' || mode === 'foreground' ? 'foreground' : 'background'
      return
    }

    const requestID = ++generation
    active = true
    void task(requestID, mode).catch(() => {
      // Tasks own their user-facing error state. The controller must still
      // release the serialization lock if a caller forgets to catch one.
    }).finally(() => {
      active = false
      if (queuedMode && !stopped) {
        const nextMode = queuedMode
        queuedMode = undefined
        request(nextMode)
      }
    })
  }

  return {
    request,
    invalidate() {
      if (stopped) return
      // Invalidation fences the active result because it belongs to the
      // previous tenant/resource identity. The replacement is foreground so a
      // context switch cannot be hidden by a background retry.
      generation += 1
      if (active) queuedMode = 'foreground'
    },
    stop() {
      stopped = true
      generation += 1
      queuedMode = undefined
    },
    isCurrent(requestID) {
      return !stopped && requestID === generation
    },
  }
}
