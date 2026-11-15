// @vitest-environment happy-dom
import { createApp, nextTick } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({ getEdge: vi.fn(), listEdgeServices: vi.fn() }))
vi.mock('./api', () => api)
import Detail from './Detail.vue'

const settle = async () => { for (let i = 0; i < 12; i++) { await Promise.resolve(); await nextTick() } }
afterEach(() => { vi.useRealTimers(); vi.resetAllMocks(); document.body.innerHTML = '' })

it('retains services without an in-flow alert on a failed poll, and announces an explicit refresh failure', async () => {
  vi.useFakeTimers()
  api.getEdge.mockResolvedValue({ name: 'demo', type: 'server', connected: true, phase: 'Ready', conditions: [], spec: { labels: {} }, rawObject: { metadata: { name: 'demo' } } })
  api.listEdgeServices.mockResolvedValueOnce([{ name: 'example', serviceType: 'Example', phase: 'Ready' }])
    .mockRejectedValueOnce(new Error('Poll failed'))
    .mockRejectedValueOnce(new Error('Refresh failed'))
  const host = document.createElement('railgrid-provider-edges')
  document.body.append(host)
  const app = createApp(Detail, { name: 'demo', type: 'server', cluster: 'tenant', token: null })
  app.mount(host)
  try {
    await settle()
    const buttons = () => [...host.querySelectorAll('button')]
    const status = host.querySelector('#edge-services [role="status"]')!
    expect(status.textContent).toBe('')
    buttons().find(button => button.textContent?.includes('Manage services'))!.click()
    await settle()
    expect(host.textContent).toContain('Example')
    await vi.advanceTimersByTimeAsync(30_000)
    await settle()
    const section = host.querySelector('#edges-services-content')!
    const card = host.querySelector('#edge-services')!
    expect(section.textContent).toContain('Example')
    expect(card.querySelector('[role="alert"]')).toBeNull()
    expect(card.querySelector('[role="status"]')).toBe(status)
    expect(status.textContent).toContain('Poll failed')
    expect(card.querySelector('[role="status"]')?.classList.contains('edge-detail__sr-only')).toBe(true)
    const refresh = buttons().find(button => button.textContent?.trim() === 'Refresh')!
    refresh.focus()
    refresh.click()
    await settle()
    expect(section.textContent).toContain('Example')
    expect(card.querySelector('[role="alert"]')?.textContent).toContain('Refresh failed')
    expect(document.activeElement).toBe(refresh)
  } finally { app.unmount() }
})

it('reports initial and foreground errors while collapsed and keeps failed polls polite', async () => {
  vi.useFakeTimers()
  api.getEdge.mockResolvedValue({ name: 'demo', type: 'server', connected: true, phase: 'Ready', conditions: [], spec: { labels: {} }, rawObject: { metadata: { name: 'demo' } } })
  api.listEdgeServices.mockRejectedValueOnce(new Error('Initial failure'))
    .mockResolvedValueOnce([{ name: 'example', serviceType: 'Example', phase: 'Ready' }])
    .mockRejectedValueOnce(new Error('Poll failure'))
    .mockRejectedValueOnce(new Error('Refresh failure'))
  const host = document.createElement('railgrid-provider-edges')
  document.body.append(host)
  const app = createApp(Detail, { name: 'demo', type: 'server', cluster: 'tenant', token: null })
  app.mount(host)
  try {
    await settle()
    const card = host.querySelector('#edge-services')!
    expect(host.querySelector('#edges-services-content')).toBeNull()
    expect(card.querySelector('[role="alert"]')?.textContent).toContain('Initial failure')
    const refresh = [...host.querySelectorAll('button')].find(button => button.textContent?.trim() === 'Refresh')!
    refresh.click()
    await settle()
    expect(card.querySelector('[role="alert"]')).toBeNull()
    await vi.advanceTimersByTimeAsync(30_000)
    await settle()
    expect(host.querySelector('#edges-services-content')).toBeNull()
    expect(card.querySelector('[role="status"]')?.textContent).toContain('Poll failure')
    expect(card.querySelector('[role="status"]')?.classList.contains('edge-detail__sr-only')).toBe(true)
    refresh.focus()
    refresh.click()
    await settle()
    expect(card.querySelector('[role="alert"]')?.textContent).toContain('Refresh failure')
    expect(document.activeElement).toBe(refresh)
  } finally { app.unmount() }
})

it.each(['edge', 'services'] as const)('recovers from a stalled %s read and ignores its late result', async stalled => {
  vi.useFakeTimers()
  const edge = (hostname: string) => ({ name: 'demo', type: 'server', hostname, connected: true, phase: 'Ready', conditions: [], spec: { labels: {} }, rawObject: { metadata: { name: 'demo' } } })
  const services = (name: string) => [{ name, serviceType: name, phase: 'Ready' }]
  let resolveLate!: (value: any) => void
  const late = new Promise(resolve => { resolveLate = resolve })
  api.getEdge.mockResolvedValue(edge('initial'))
  api.listEdgeServices.mockResolvedValue(services('initial-service'))
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(Detail, { name: 'demo', type: 'server', cluster: 'tenant', token: null })
  app.mount(host)
  try {
    await settle()
    ;[...host.querySelectorAll('button')].find(button => button.textContent?.includes('Manage services'))!.click()
    await settle()
    if (stalled === 'edge') api.getEdge.mockReturnValueOnce(late)
    else api.listEdgeServices.mockReturnValueOnce(late)
    const refresh = [...host.querySelectorAll('button')].find(button => button.textContent?.trim() === 'Refresh')!
    refresh.click()
    await settle()
    await vi.advanceTimersByTimeAsync(15_000)
    await settle()
    const timedOutSignal = stalled === 'edge' ? api.getEdge.mock.calls[1][2] : api.listEdgeServices.mock.calls[1][1]
    expect(timedOutSignal.aborted).toBe(true)
    expect(host.textContent).toContain(stalled === 'edge' ? 'edge read timed out' : 'services read timed out')
    expect(host.textContent).toContain('initial-service')
    api.getEdge.mockResolvedValue(edge('new-host'))
    api.listEdgeServices.mockResolvedValue(services('new-service'))
    refresh.click()
    await settle()
    expect(host.textContent).toContain('new-host')
    expect(host.textContent).toContain('new-service')
    resolveLate(stalled === 'edge' ? edge('late-host') : services('late-service'))
    await settle()
    expect(host.textContent).not.toContain('late-host')
    expect(host.textContent).not.toContain('late-service')
    expect(host.textContent).toContain('new-service')
  } finally { app.unmount() }
})
