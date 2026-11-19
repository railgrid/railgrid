import { reactive } from 'vue'

export { createAdaptiveRefreshTimer, createLatestRefreshController, FAST_REFRESH_MS, STABLE_REFRESH_MS } from './portalkit/refresh'
export type { AdaptiveRefreshTimer, LatestRefreshController, ResourceRefreshMode } from './portalkit/refresh'

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
