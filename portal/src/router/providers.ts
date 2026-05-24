// Provider routes are mostly static — /providers shows the catalog, and a
// single parameterized route /providers/:name/:rest(.*)* matches every
// provider page below. We keep the dynamic-route trick for adapt to
// per-provider components later, but for Phase 2 the ProviderFrame.vue
// component handles every provider uniformly via the :name param.

import type { Router } from 'vue-router'

let registered = false

// registerProviderRoutes installs the dynamic provider matcher exactly once.
// Idempotent so multiple store refreshes are safe.
export function registerProviderRoutes(router: Router) {
  if (registered) return
  router.addRoute({
    path: '/providers/:name/:rest(.*)*',
    name: 'provider-frame',
    component: () => import('@/pages/ProviderFrame.vue'),
    props: (route) => ({
      providerName: route.params.name as string,
      subPath: Array.isArray(route.params.rest)
        ? route.params.rest.join('/')
        : (route.params.rest as string) ?? '',
    }),
  })
  registered = true
}
