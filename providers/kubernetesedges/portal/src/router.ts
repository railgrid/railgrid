// Internal router for the kubernetes-edges custom element. Memory
// history (no URL collision with the portal SPA's HTML5 history); the
// element bridges navigations to/from the portal via kedge-navigate
// CustomEvents and the host's reactive subPath prop.
//
// Route layout mirrors the legacy in-tree portal routes so the user-
// visible URL structure stays the same:
//
//   /                          (was /edges)              EdgesPage(kubernetes)
//   /:name                     (was /edges/:name)        EdgeDetailPage
//   /workloads                 (was /workloads)          WorkloadsPage
//   /workloads/:ns/:name       (was /workloads/:ns/:n)   WorkloadDetailPage

import { createRouter, createMemoryHistory, type Router } from 'vue-router'

import EdgesPage from './EdgesPage.vue'
import EdgeDetailPage from './EdgeDetailPage.vue'
import WorkloadsPage from './WorkloadsPage.vue'
import WorkloadDetailPage from './WorkloadDetailPage.vue'
import TerminalPage from './TerminalPage.vue'

export function createInternalRouter(initial: string): Router {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', name: 'edges-list', component: EdgesPage, props: { kind: 'kubernetes' } },
      { path: '/workloads', name: 'workloads-list', component: WorkloadsPage },
      { path: '/workloads/:namespace/:name', name: 'workload-detail', component: WorkloadDetailPage, props: true },
      // Deep-link helper that opens a terminal session and bounces back
      // to the edge detail page. Registered before /:name so the more-
      // specific path wins.
      { path: '/:name/terminal', name: 'edge-terminal', component: TerminalPage, props: true },
      // :name is registered LAST so it doesn't shadow /workloads* or
      // /:name/terminal. Vue Router matches routes in registration
      // order for ambiguous shapes.
      { path: '/:name', name: 'edge-detail', component: EdgeDetailPage, props: true },
    ],
  })
  router.replace(initial || '/')
  return router
}
