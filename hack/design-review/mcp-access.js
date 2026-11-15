import { createApp, h } from 'vue'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter, RouterView } from 'vue-router'
import '@fontsource-variable/instrument-sans'
import '@fontsource/ibm-plex-mono/400.css'
import './mcp-access.css'
import MCPPage from '../../portal/src/pages/MCPPage.vue'
import ConfirmDialog from '../../portal/src/portalkit/ConfirmDialog.vue'
import { useTenantStore } from '../../portal/src/stores/tenant'

// Intercept every request: this fixture must never contact a hub or use credentials.
const orgUUID = '11111111-1111-4111-8111-111111111111'
const workspaceUUID = '22222222-2222-4222-8222-222222222222'
const scenario = new URLSearchParams(location.search).get('scenario') || 'ready'
const servers = [{ name: 'operations', uid: 'sample-uid', displayName: 'Operations', phase: 'Ready', readOnly: false, toolsRefreshedTime: '2026-10-01T12:00:00Z', federatedProviders: scenario === 'empty' ? [] : [
  { name: 'edges', displayName: 'Edge services', reachable: true, tools: [{ name: 'list_services', title: 'List services', description: 'Find services available on connected edges.' }, { name: 'get_service', title: 'Get service', description: 'Inspect a service.' }] },
  { name: 'code', displayName: 'Code', reachable: false, message: 'Provider is temporarily unreachable.', tools: [] },
] }]
window.fetch = async (input, init = {}) => {
  const path = String(input)
  if (path === '/api/orgs') return Response.json({ items: [{ uuid: orgUUID, name: 'Review organization' }] })
  if (path === `/api/orgs/${orgUUID}/workspaces`) return Response.json({ items: [{ uuid: workspaceUUID, name: 'Review workspace', clusterName: 'review-cluster' }] })
  if (path.includes('/mcpservers')) {
    if (init.method === 'DELETE') return new Response(null, { status: 409 })
    if (init.method === 'POST') {
      const created = JSON.parse(init.body)
      servers.push({ ...created, uid: 'sample-created-uid', phase: 'Ready', federatedProviders: [] })
      return Response.json({ name: created.name }, { status: 201 })
    }
    if (path.endsWith('/connect')) {
      if (scenario === 'connect-failure') return new Response(null, { status: 503 })
      return Response.json({ serverName: 'operations', endpointURL: 'https://example.invalid/mcp/operations', token: 'synthetic-token-not-a-credential', tokenReady: scenario !== 'pending' })
    }
    return Response.json({ items: servers })
  }
  return Response.json({ items: [] })
}
const pinia = createPinia()
export const router = createRouter({ history: createMemoryHistory(), routes: [
  { path: '/mcp', name: 'mcp', component: MCPPage },
  { path: '/mcp/create', name: 'mcp-create', component: MCPPage },
  { path: '/mcp/:name', name: 'mcp-detail', component: MCPPage },
] })
export const app = createApp({ render: () => h('div', [
  h('div', { style: 'padding:12px 24px; display:flex; gap:12px; flex-wrap:wrap; align-items:center' }, [
    h('strong', 'Synthetic MCP review — no hub calls or real credentials'),
    ...['ready', 'pending', 'connect-failure', 'empty'].map(value => h('a', { href: `?scenario=${value}`, 'aria-current': scenario === value ? 'page' : undefined }, value)),
    h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => document.documentElement.classList.toggle('light') }, 'Toggle theme'),
  ]),
  h(RouterView), h(ConfirmDialog),
]) })
app.use(pinia)
app.use(router)
const tenant = useTenantStore(pinia)
tenant.orgUUID = orgUUID
tenant.workspaceUUID = workspaceUUID
router.push(new URLSearchParams(location.search).get('view') === 'collection' ? '/mcp' : '/mcp/operations')
export const mounted = router.isReady().then(() => app.mount('#app'))
