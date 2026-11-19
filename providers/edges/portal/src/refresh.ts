import { reactive } from 'vue'
import type { EdgeService } from './types'

export { createAdaptiveRefreshTimer, createLatestRefreshController, FAST_REFRESH_MS, STABLE_REFRESH_MS } from './portalkit/refresh'
export type { AdaptiveRefreshTimer, LatestRefreshController, ResourceRefreshMode } from './portalkit/refresh'

type ServiceIdentity = Pick<EdgeService, 'name' | 'uid'>

export function createServiceDeletions() {
  const pending = reactive(new Map<string, string>())
  return {
    acknowledge(name: string, uid: string) {
      pending.set(name, uid)
    },
    has(service: ServiceIdentity) {
      const uid = pending.get(service.name)
      return !!uid && (!service.uid || service.uid === uid)
    },
    reconcile(services: readonly ServiceIdentity[], complete: boolean) {
      const byName = new Map(services.map(service => [service.name, service]))
      for (const [name, uid] of pending) {
        const service = byName.get(name)
        if ((!service && complete) || (service?.uid && service.uid !== uid)) pending.delete(name)
      }
    },
    clear() {
      pending.clear()
    },
  }
}

export type ServiceDeletions = ReturnType<typeof createServiceDeletions>
