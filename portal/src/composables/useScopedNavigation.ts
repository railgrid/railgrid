import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useTenantStore } from '@/stores/tenant'
import { scopedPath, portalRoutePath } from '@/portalkit/navigation'

export function useScopedNavigation() {
  const tenant = useTenantStore()
  const route = useRoute()
  return {
    scopePath: (path: string) => scopedPath(path, tenant),
    routePath: computed(() => portalRoutePath(route.path)),
  }
}
