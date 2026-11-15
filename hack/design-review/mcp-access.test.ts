import { createApp, h, nextTick } from 'vue'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { expect, it, onTestFinished, vi } from 'vitest'
import MCPPage from '../../portal/src/pages/MCPPage.vue'
import { useTenantStore } from '../../portal/src/stores/tenant'

const request = vi.hoisted(() => vi.fn())
vi.mock('../../portal/src/auth/session', () => ({ authFetch: request }))
vi.mock('../../portal/src/components/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))

it('opens a server and exposes pending token and keyboard client tabs', async () => {
  request.mockImplementation(async (path: string) => path.endsWith('/connect')
    ? Response.json({ serverName: 'operations', endpointURL: 'https://example.invalid/mcp', token: 'synthetic', tokenReady: false })
    : Response.json({ items: [{ name: 'operations', uid: 'sample', phase: 'Ready', federatedProviders: [{ name: 'edges', reachable: true, tools: [{ name: 'list_services' }] }] }] }))
  const pinia = createPinia()
  const router = createRouter({ history: createMemoryHistory(), routes: [
    { path: '/mcp', name: 'mcp', component: MCPPage },
    { path: '/mcp/:name', name: 'mcp-detail', component: MCPPage },
    { path: '/create', name: 'mcp-create', component: MCPPage },
  ] })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(MCPPage).use(pinia).use(router)
  const tenant = useTenantStore(pinia)
  tenant.orgUUID = 'org'; tenant.workspaceUUID = 'workspace'
  await router.push('/mcp')
  await router.isReady()
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  await vi.waitFor(() => expect(host.textContent).toContain('operations'))
  await router.push('/mcp/operations')
  await vi.waitFor(() => expect(host.textContent).toMatch(/token.*(pending|provision)/i))
  const tabs = host.querySelectorAll<HTMLButtonElement>('[role=tab]')
  expect(tabs.length).toBe(3)
  tabs[0].focus()
  tabs[0].dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }))
  await nextTick()
  expect(document.activeElement).toBe(tabs[1])
  expect(tabs[1].getAttribute('aria-selected')).toBe('true')
})
