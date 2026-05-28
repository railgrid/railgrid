// Internal router for the server-edges custom element. Same shape as
// kubernetes-edges (memory history, root + detail + terminal helper),
// just with `kind: 'server'` baked into the list page so EdgesPage
// filters down to server-type edges and EdgeCreateModal locks the type.
//
// EdgesPage / EdgeDetailPage / TerminalPage are cross-imported from
// the kubernetes-edges source tree via the @kedge-edges alias — they
// are shared "edge primitives" both providers render.

import { createRouter, createMemoryHistory, type Router } from 'vue-router'

import EdgesPage from '@kedge-edges/EdgesPage.vue'
import EdgeDetailPage from '@kedge-edges/EdgeDetailPage.vue'
import TerminalPage from '@kedge-edges/TerminalPage.vue'

export function createInternalRouter(initial: string): Router {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', name: 'servers-list', component: EdgesPage, props: { kind: 'server' } },
      { path: '/:name/terminal', name: 'server-terminal', component: TerminalPage, props: true },
      { path: '/:name', name: 'server-detail', component: EdgeDetailPage, props: true },
    ],
  })
  router.replace(initial || '/')
  return router
}
