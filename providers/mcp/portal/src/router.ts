// Internal router for the MCP custom element. Uses memory history so it
// doesn't fight with the portal SPA's HTML5 history. The element's
// shell (see element.ts) wires it to the kedge-navigate event the
// portal's ProviderFrame listens for, and seeds the initial route from
// kedgeContext.basePath / subPath on first mount.

import { createRouter, createMemoryHistory, type Router } from 'vue-router'

import MCPPage from './MCPPage.vue'
import MCPDetailPage from './MCPDetailPage.vue'

export function createInternalRouter(initial: string): Router {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', name: 'mcp-list', component: MCPPage },
      // Detail page param shape matches the existing MCPDetailPage
      // useRoute().params.name + useRoute().query.kind reads.
      { path: '/:name', name: 'mcp-detail', component: MCPDetailPage, props: true },
    ],
  })
  // Initial navigation; without this the router stays at "start" until
  // first push and <router-view /> renders nothing.
  router.replace(initial || '/')
  return router
}
