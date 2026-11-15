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

it('keeps an unlisted saved type selectable and applies grouped Edit service choices', async () => {
  const service = { name: 'demo', serviceType: 'legacy-type', scheme: 'http', port: 9443, edgeName: 'edge', conditions: [] }
  const catalog = [
    { type: 'home-assistant', displayName: 'Home Assistant', category: 'Home', auth: 'none', credential: { fields: [] } },
    { type: 'unifi-network', displayName: 'UniFi Network', category: 'Network', auth: 'none', credential: { fields: [] }, defaultScheme: 'https', defaultPort: 443, schemeLocked: true },
  ]
  api.getService.mockResolvedValue(service)
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(ServiceEdit, { serviceName: service.name, catalog, edges: [] })
  app.mount(host)
  try {
    await settle()
    const type = host.querySelector<HTMLButtonElement>('#service-edit-type')!
    const scheme = host.querySelector<HTMLButtonElement>('#service-edit-scheme')!
    const port = host.querySelector<HTMLInputElement>('#service-edit-port')!
    expect(type.textContent).toContain('legacy-type')
    expect(port.value).toBe('9443')
    scheme.click()
    await settle()
    document.querySelectorAll<HTMLButtonElement>('#service-edit-scheme-listbox [role="option"]')[1].click()
    await settle()
    expect(scheme.textContent).toContain('https')

    type.focus()
    type.click()
    await settle()
    const listbox = document.getElementById('service-edit-type-listbox')!
    expect(listbox.querySelector('[aria-selected="true"]')?.textContent).toContain('legacy-type')
    const groups = [...listbox.querySelectorAll('[role="group"]')]
    expect(groups.map(group => document.getElementById(group.getAttribute('aria-labelledby')!)?.textContent)).toEqual(['Home', 'Network'])
    type.dispatchEvent(new KeyboardEvent('keydown', { key: 'End', bubbles: true }))
    type.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await settle()
    expect(type.textContent).toContain('UniFi Network')
    expect(document.activeElement).toBe(type)
    expect(scheme.disabled).toBe(true)
    expect(scheme.getAttribute('aria-describedby')).toBe('service-edit-scheme-help')
    expect(port.value).toBe('443')
    expect(api.updateEdgeService).not.toHaveBeenCalled()
  } finally { app.unmount() }
})
