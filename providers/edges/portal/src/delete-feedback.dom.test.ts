// @vitest-environment happy-dom
import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
const api = vi.hoisted(() => ({
  setHostFetch: vi.fn(), setToken: vi.fn(), setTenant: vi.fn(),
  listEdges: vi.fn(), listServicesPage: vi.fn(), listWorkloadsPage: vi.fn(),
  listServices: vi.fn(), listWorkloads: vi.fn(), fetchServiceCatalog: vi.fn(),
  deleteEdge: vi.fn(), deleteEdgeService: vi.fn(), deleteWorkload: vi.fn(),
}))
vi.mock('./api', () => api)
vi.mock('./portalkit/confirm', async (original) => ({ ...await original<typeof import('./portalkit/confirm')>(), confirmDialog: vi.fn().mockResolvedValue(true) }))
vi.mock('./portalkit/toast', () => ({ toast: vi.fn() }))
import App from './App.vue'
import Services from './Services.vue'
import Workloads from './Workloads.vue'
const edge = { name: 'edge-a', type: 'kubernetes', connected: true }
const service = { name: 'service-a', edgeName: 'edge-a' }
const workload = { name: 'workload-a' }
const settle = async () => { for (let i = 0; i < 5; i++) { await Promise.resolve(); await nextTick() } }
afterEach(() => { document.body.innerHTML = ''; vi.clearAllMocks() })
it.each([
  ['edge', App, edge, 'deleteEdge', { ctx: { tenant: 'tenant-a', user: { userId: 'user-a' }, subPath: '' } }],
  ['service', Services, service, 'deleteEdgeService', {}],
  ['workload', Workloads, workload, 'deleteWorkload', {}],
] as const)('dismisses %s deletion feedback while retaining read failure', async (_kind, component, resource, method, props) => {
  api.listEdges.mockResolvedValue([edge])
  api.listServicesPage.mockResolvedValue({ items: [service] })
  api.listWorkloadsPage.mockResolvedValue({ items: [workload] })
  api.fetchServiceCatalog.mockResolvedValue([])
  api[method].mockRejectedValue(new Error('Deletion denied'))
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(component, props)
  const vm = app.mount(host) as any
  try {
    await settle()
    const state = vm.$.setupState
    state.error = 'Read unavailable'
    await state.onDelete(resource)
    await settle()
    expect(host.textContent).toContain('Deletion denied')
    const dismiss = host.querySelector<HTMLButtonElement>('button[aria-label="Dismiss deletion error"]')!
    expect(dismiss).toBeTruthy()
    dismiss.focus()
    dismiss.click()
    await settle()
    expect(host.textContent).not.toContain('Deletion denied')
    expect(state.error).toBe('Read unavailable')
    expect(document.activeElement).toBe(host.querySelector('h1'))
  } finally { app.unmount() }
})
