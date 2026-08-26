import { reactive } from 'vue'
import type { ResourceRefreshMode } from './portalkit/page-state'

export type { ResourceRefreshMode } from './portalkit/page-state'

export interface LatestRefreshController {
  request(mode?: ResourceRefreshMode): void
  invalidate(): void
  stop(): void
  isCurrent(requestID: number): boolean
}

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

export type OperationPhase = 'creating' | 'saving' | 'deleting'

export interface OperationLocks {
  acquire(key: string, phase?: OperationPhase): boolean
  release(key: string): void
  isLocked(key: string): boolean
  phase(key: string): OperationPhase | undefined
}

// Operation ownership is local to a mounted route. App.vue remounts routes when
// tenant/token or resource identity changes, preventing old-context mutations
// from leaking locks into the new route.
export function createOperationLocks(): OperationLocks {
  const locked = reactive(new Map<string, OperationPhase>())
  return {
    acquire(key, phase = 'saving') {
      if (locked.has(key)) return false
      locked.set(key, phase)
      return true
    },
    release(key) {
      locked.delete(key)
    },
    isLocked(key) {
      return locked.has(key)
    },
    phase(key) {
      return locked.get(key)
    },
  }
}

export interface ResourceDeletions {
  acknowledge(scope: string, name: string, uid?: string): void
  has(scope: string, name: string, uid?: string): boolean
  reconcile(scope: string, resources: readonly { name: string; uid?: string }[]): void
  clear(): void
}

// Acknowledged deletes outlive route-local operation locks. The scope is the
// complete authoritative list boundary: workspace-wide for top-level resources
// and repository-specific for DeployKeys and Collaborators.
export function createResourceDeletions(): ResourceDeletions {
  const identities = reactive(new Map<string, string | null>())
  const scopedKey = (scope: string, name: string) => `${scope}:${name}`
  return {
    acknowledge(scope, name, uid) {
      identities.set(scopedKey(scope, name), uid ?? null)
    },
    has(scope, name, uid) {
      const key = scopedKey(scope, name)
      if (!identities.has(key)) return false
      const expectedUID = identities.get(key)
      return expectedUID === null ? uid === undefined : uid === undefined || expectedUID === uid
    },
    reconcile(scope, resources) {
      const present = new Map(resources.map(resource => [resource.name, resource.uid]))
      const prefix = `${scope}:`
      for (const [key, expectedUID] of [...identities]) {
        if (!key.startsWith(prefix)) continue
        const name = key.slice(prefix.length)
        const currentUID = present.get(name)
        if (!present.has(name) ||
          (expectedUID !== null && currentUID !== undefined && currentUID !== expectedUID) ||
          (expectedUID === null && currentUID !== undefined)) {
          identities.delete(key)
        }
      }
    },
    clear() {
      identities.clear()
    },
  }
}

export function operationKey(kind: string, name: string): string {
  return `${kind}:${name}`
}

// Serializes timer/manual/mutation refreshes. A request made while one is in
// flight queues one latest read without fencing the active read. This lets a
// timer observe the current snapshot instead of starving it, while a queued
// foreground request still wins over any queued background request.
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
      queuedMode = queuedMode === 'foreground' || mode === 'foreground' ? 'foreground' : 'background'
      return
    }
    const requestID = ++generation
    active = true
    void task(requestID, mode).catch(() => {
      // Tasks own user-facing error state.
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
      // Unlike an ordinary queued request, invalidation fences the active
      // result: it belongs to the previous tenant/resource identity. The
      // replacement is foreground so a context switch cannot be hidden by a
      // background retry.
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
