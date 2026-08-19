import { reactive } from 'vue'

export interface LatestRefreshController {
  request(): void
  invalidate(): void
  stop(): void
  isCurrent(requestID: number): boolean
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
// flight supersedes that result and queues one latest read, so stale responses
// cannot overwrite newer state.
export function createLatestRefreshController(
  task: (requestID: number) => Promise<void>,
): LatestRefreshController {
  let generation = 0
  let active = false
  let queued = false
  let stopped = false

  const request = () => {
    if (stopped) return
    if (active) {
      generation += 1
      queued = true
      return
    }
    const requestID = ++generation
    active = true
    void task(requestID).catch(() => {
      // Tasks own user-facing error state.
    }).finally(() => {
      active = false
      if (queued && !stopped) {
        queued = false
        request()
      }
    })
  }

  return {
    request,
    invalidate() {
      if (stopped) return
      generation += 1
      if (active) queued = true
    },
    stop() {
      stopped = true
      generation += 1
      queued = false
    },
    isCurrent(requestID) {
      return !stopped && requestID === generation
    },
  }
}
