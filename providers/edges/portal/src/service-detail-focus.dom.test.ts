// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
const api = vi.hoisted(() => ({ getService: vi.fn(), connectEdgeService: vi.fn(), deleteEdgeService: vi.fn(), updateEdgeService: vi.fn(), listEdges: vi.fn(), fetchServiceCatalog: vi.fn(), listServicesPage: vi.fn(), setTenant: vi.fn(), setToken: vi.fn(), setHostFetch: vi.fn() }))
vi.mock('./api', async importOriginal => ({ ...await importOriginal<typeof import('./api')>(), ...api }))
import ServiceEdit from './ServiceEdit.vue'
import App from './App.vue'
const settle = async () => { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
afterEach(() => { document.body.innerHTML = ''; vi.resetAllMocks() })
it.each(['success', 'moved', 'scope'] as const)('service retry focus: %s', async outcome => {
  api.getService.mockRejectedValueOnce(new Error('Unavailable'))
  let resolve!: (value: any) => void
  api.getService.mockReturnValueOnce(new Promise(r => { resolve = r }))
  const generation = ref(1)
  const host = document.createElement('div')
  const outside = document.createElement('button')
  document.body.append(host, outside)
  const app = createApp({ render: () => h(ServiceEdit, { serviceName: 'demo', authorityGeneration: generation.value, catalog: [], edges: [] }) })
  app.mount(host)
  try {
    await settle()
    const retry = host.querySelector<HTMLButtonElement>('.k-resource-page__retry')!
    expect(retry).toBeTruthy()
    retry.focus()
    if (outcome === 'moved') outside.focus()
    if (outcome === 'scope') { generation.value++; await nextTick() }
    retry.click()
    await settle()
    resolve({ name: 'demo', serviceType: 'test', edgeName: 'edge', conditions: [] })
    await settle()
    expect(document.activeElement).toBe(outcome === 'success' ? host.querySelector('.k-resource-page__title') : outcome === 'moved' ? outside : document.body)
  } finally { app.unmount() }
})

it('propagates the app authority to service retry focus across a tenant change', async () => {
  api.listEdges.mockResolvedValue([])
  api.fetchServiceCatalog.mockResolvedValue([])
  api.listServicesPage.mockResolvedValue({ items: [] })
  let resolveOld!: (value: any) => void
  api.getService.mockRejectedValueOnce(new Error('Unavailable in A'))
    .mockReturnValueOnce(new Promise(r => { resolveOld = r }))
    .mockRejectedValueOnce(new Error('Unavailable in B'))
    .mockResolvedValueOnce({ name: 'demo', serviceType: 'test', edgeName: 'edge', conditions: [] })
  const state = reactive({ ctx: { tenant: 'tenant-a', token: 'token', user: { userId: 'user-a' }, subPath: 'services/demo' } })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx: state.ctx }) })
  app.mount(host)
  try {
    await settle()
    const oldRetry = host.querySelector<HTMLButtonElement>('.k-resource-page__retry')!
    expect(oldRetry).toBeTruthy()
    oldRetry.focus(); oldRetry.click(); await settle()
    state.ctx = { ...state.ctx, tenant: 'tenant-b' }; await settle()
    const newRetry = host.querySelector<HTMLButtonElement>('.k-resource-page__retry')!
    expect(newRetry).toBeTruthy()
    expect(newRetry).not.toBe(oldRetry)
    newRetry.focus()
    resolveOld({ name: 'demo', serviceType: 'old', edgeName: 'edge', conditions: [] }); await settle()
    expect(document.activeElement).toBe(newRetry)
    newRetry.click(); await settle()
    expect(document.activeElement).toBe(host.querySelector('.k-resource-page__title'))
  } finally { app.unmount() }
})
