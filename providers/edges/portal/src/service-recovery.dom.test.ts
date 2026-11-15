// @vitest-environment happy-dom
import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
const api = vi.hoisted(() => ({ listEdges: vi.fn(), fetchServiceCatalog: vi.fn(), createKubeEdgeService: vi.fn() }))
vi.mock('./api', () => api)
import ServiceCreate from './ServiceCreate.vue'
const edge = { name: 'edge-a', type: 'kubernetes' }
const settle = async () => { await Promise.resolve(); await nextTick(); await Promise.resolve(); await nextTick() }
const deferred = () => {
  let resolve!: (value: never[]) => void
  let reject!: (error: Error) => void
  const promise = new Promise<never[]>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
afterEach(() => { document.body.innerHTML = ''; vi.resetAllMocks() })
it.each(['success', 'failure', 'moved'] as const)('retains retry focus and edited values: %s', async outcome => {
  api.fetchServiceCatalog.mockRejectedValueOnce(new Error('Catalog unavailable'))
  api.listEdges.mockResolvedValue([edge])
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(ServiceCreate)
  app.mount(host)
  try {
    await settle()
    const port = host.querySelector('#service-create-port') as HTMLInputElement
    port.value = '4444'
    port.dispatchEvent(new Event('input', { bubbles: true }))
    const scheme = [...host.querySelectorAll('select')].find(s => s.textContent?.includes('https'))!
    scheme.value = 'https'
    scheme.dispatchEvent(new Event('change', { bubbles: true }))
    const retry = [...host.querySelectorAll('button')].find(b => b.textContent?.includes('Retry service'))!
    const pending = deferred()
    api.fetchServiceCatalog.mockReturnValueOnce(pending.promise)
    retry.focus()
    retry.click()
    await settle()
    expect(document.activeElement).toBe(retry)
    expect(retry.getAttribute('aria-disabled')).toBe('true')
    expect(host.querySelector('#service-create-port')).toBe(port)
    retry.click()
    expect(api.fetchServiceCatalog).toHaveBeenCalledTimes(2)
    const back = host.querySelector('.k-back-action') as HTMLButtonElement
    if (outcome === 'moved') back.focus()
    if (outcome === 'failure') pending.reject(new Error('Still unavailable'))
    else pending.resolve([])
    await settle()
    expect(port.value).toBe('4444')
    expect(scheme.value).toBe('https')
    if (outcome === 'failure') expect(document.activeElement).toBe(retry)
    else if (outcome === 'moved') expect(document.activeElement).toBe(back)
    else expect(document.activeElement).toBe(host.querySelector('h1'))
  } finally { app.unmount() }
})
it('applies fallback defaults to untouched fields after recovery', async () => {
  api.fetchServiceCatalog.mockRejectedValueOnce(new Error('Unavailable')).mockResolvedValueOnce([])
  api.listEdges.mockResolvedValue([edge])
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(ServiceCreate)
  app.mount(host)
  try {
    await settle()
    ;[...host.querySelectorAll('button')].find(b => b.textContent?.includes('Retry service'))!.click()
    await settle()
    expect((host.querySelector('#service-create-port') as HTMLInputElement).value).toBe('80')
  } finally { app.unmount() }
})
it('applies a recovered type’s locked scheme even if the draft was edited', async () => {
  api.fetchServiceCatalog.mockRejectedValueOnce(new Error('Unavailable')).mockResolvedValueOnce([
    { type: 'locked', displayName: 'Locked', defaultPort: 443, defaultScheme: 'https', schemeLocked: true, auth: 'none', credential: { fields: [] } },
  ])
  api.listEdges.mockResolvedValue([edge])
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(ServiceCreate)
  app.mount(host)
  try {
    await settle()
    const scheme = [...host.querySelectorAll('select')].find(s => s.textContent?.includes('https'))!
    scheme.value = 'http'
    scheme.dispatchEvent(new Event('change', { bubbles: true }))
    ;[...host.querySelectorAll('button')].find(b => b.textContent?.includes('Retry service'))!.click()
    await settle()
    expect(scheme.value).toBe('https')
    expect(scheme.disabled).toBe(true)
  } finally { app.unmount() }
})
it.each([
  ['http://example.test:8080', 'example.test', 8080],
  ['http://example.test:80', 'example.test', 80],
  ['http://example.test', 'example.test', 443],
  ['http://[::1]:80', '[::1]', 80],
  ['http://[::1]', '[::1]', 443],
])('keeps a locked scheme when parsing %s', async (url, expectedHost, expectedPort) => {
  api.fetchServiceCatalog.mockResolvedValue([
    { type: 'locked', displayName: 'Locked', defaultPort: 443, defaultScheme: 'https', schemeLocked: true, auth: 'none', credential: { fields: [] } },
  ])
  api.listEdges.mockResolvedValue([{ name: 'server-a', type: 'server' }])
  api.createKubeEdgeService.mockResolvedValue(undefined)
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(ServiceCreate)
  app.mount(host)
  try {
    await settle()
    const name = host.querySelector('input[placeholder="home-assistant"]') as HTMLInputElement
    name.value = 'example'
    name.dispatchEvent(new Event('input', { bubbles: true }))
    const hostInput = host.querySelector('input[aria-labelledby="service-create-host-label"]') as HTMLInputElement
    hostInput.value = url
    hostInput.dispatchEvent(new Event('input', { bubbles: true }))
    hostInput.dispatchEvent(new Event('blur'))
    await settle()
    const scheme = [...host.querySelectorAll('select')].find(s => s.textContent?.includes('https'))!
    expect(scheme.value).toBe('https')
    expect(hostInput.value).toBe(expectedHost)
    expect((host.querySelector('#service-create-port') as HTMLInputElement).value).toBe(String(expectedPort))
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settle()
    expect(api.createKubeEdgeService).toHaveBeenCalledWith(expect.objectContaining({ scheme: 'https', host: expectedHost, port: expectedPort }), expect.any(AbortSignal))
  } finally { app.unmount() }
})

it.each([
  ['', undefined],
  ['example.test', 'example.test'],
  ['192.168.1.2', '192.168.1.2'],
  ['::1', '::1'],
  ['[::1]', '[::1]'],
])('accepts host %s without changing its meaning', async (value, expectedHost) => {
  api.fetchServiceCatalog.mockResolvedValue([])
  api.listEdges.mockResolvedValue([{ name: 'server-a', type: 'server' }])
  api.createKubeEdgeService.mockResolvedValue(undefined)
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(ServiceCreate)
  app.mount(host)
  try {
    await settle()
    const name = host.querySelector('input[placeholder="home-assistant"]') as HTMLInputElement
    name.value = 'example'
    name.dispatchEvent(new Event('input', { bubbles: true }))
    const input = host.querySelector('#service-create-host') as HTMLInputElement
    input.value = value
    input.dispatchEvent(new Event('input', { bubbles: true }))
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settle()
    expect(api.createKubeEdgeService).toHaveBeenCalledWith(expect.objectContaining({ host: expectedHost }), expect.any(AbortSignal))
  } finally { app.unmount() }
})

it.each(['http://[', 'http:/example.test', 'https//example.test', 'ftp://example.test'])('blocks malformed or unsupported URL %s and allows correction', async invalidURL => {
  api.fetchServiceCatalog.mockResolvedValue([])
  api.listEdges.mockResolvedValue([{ name: 'server-a', type: 'server' }])
  api.createKubeEdgeService.mockResolvedValue(undefined)
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(ServiceCreate)
  app.mount(host)
  try {
    await settle()
    const name = host.querySelector('input[placeholder="home-assistant"]') as HTMLInputElement
    name.value = 'example'
    name.dispatchEvent(new Event('input', { bubbles: true }))
    const input = host.querySelector('#service-create-host') as HTMLInputElement
    input.value = invalidURL
    input.dispatchEvent(new Event('input', { bubbles: true }))
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settle()
    expect(api.createKubeEdgeService).not.toHaveBeenCalled()
    expect(input.value).toBe(invalidURL)
    expect(input.getAttribute('aria-invalid')).toBe('true')
    expect(input.getAttribute('aria-describedby')).toContain('service-create-host-error')
    expect(document.activeElement).toBe(input)
    input.value = 'http://example.test:8080'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    input.dispatchEvent(new Event('blur'))
    await settle()
    expect(input.hasAttribute('aria-invalid')).toBe(false)
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settle()
    expect(api.createKubeEdgeService).toHaveBeenCalledWith(expect.objectContaining({ host: 'example.test', port: 8080 }), expect.any(AbortSignal))
  } finally { app.unmount() }
})

it('shows invalid URL feedback when the service requires a host', async () => {
  api.fetchServiceCatalog.mockResolvedValue([
    { type: 'required', displayName: 'Required host', defaultPort: 80, defaultScheme: 'http', hostRequired: true, auth: 'none', credential: { fields: [] } },
  ])
  api.listEdges.mockResolvedValue([{ name: 'server-a', type: 'server' }])
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(ServiceCreate)
  app.mount(host)
  try {
    await settle()
    const name = host.querySelector('input[placeholder="home-assistant"]') as HTMLInputElement
    name.value = 'example'
    name.dispatchEvent(new Event('input', { bubbles: true }))
    const input = host.querySelector('#service-create-host') as HTMLInputElement
    input.value = 'http://['
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await settle()
    expect((host.querySelector('button[type="submit"]') as HTMLButtonElement).disabled).toBe(false)
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settle()
    expect(api.createKubeEdgeService).not.toHaveBeenCalled()
    expect(input.getAttribute('aria-invalid')).toBe('true')
    expect(document.activeElement).toBe(input)
  } finally { app.unmount() }
})

it('keeps service type groups navigable and applies FormSelect choices', async () => {
  const credential = { fields: [] }
  api.fetchServiceCatalog.mockResolvedValue([
    { type: 'home-assistant', displayName: 'Home Assistant', category: 'Home', defaultPort: 8123, defaultScheme: 'http', credential },
    { type: 'unifi-network', displayName: 'UniFi Network', category: 'Network', defaultPort: 443, defaultScheme: 'https', schemeLocked: true, credential },
  ])
  api.listEdges.mockResolvedValue([edge, { name: edge.name, type: 'server' }])
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(ServiceCreate)
  app.mount(host)
  try {
    await settle()
    const edgeSelect = host.querySelector<HTMLButtonElement>('#service-create-edge')!
    const typeSelect = host.querySelector<HTMLButtonElement>('#service-create-type')!
    const schemeSelect = host.querySelector<HTMLButtonElement>('#service-create-scheme')!
    expect(edgeSelect.getAttribute('aria-required')).toBe('true')
    edgeSelect.click()
    await settle()
    const edgeOptions = document.querySelectorAll('#service-create-edge-listbox [role="option"]')
    expect([...edgeOptions].map(option => option.textContent?.trim())).toEqual([
      'edge-a (KubernetesCluster)', 'edge-a (LinuxServer)',
    ])
    ;(edgeOptions[1] as HTMLButtonElement).click()
    await settle()
    expect(edgeSelect.textContent).toContain('LinuxServer')

    schemeSelect.click()
    await settle()
    const schemeOptions = document.querySelectorAll<HTMLButtonElement>('#service-create-scheme-listbox [role="option"]')
    schemeOptions[1].click()
    await settle()
    expect(schemeSelect.textContent).toContain('https')

    typeSelect.focus()
    typeSelect.click()
    await settle()
    const groups = document.querySelectorAll('#service-create-type-listbox [role="group"]')
    expect([...groups].map(group => document.getElementById(group.getAttribute('aria-labelledby')!)?.textContent)).toEqual(['Home', 'Network'])
    expect(groups[0].querySelector('[role="option"]')?.textContent).toContain('Home Assistant')
    expect(groups[1].querySelector('[role="option"]')?.textContent).toContain('UniFi Network')

    typeSelect.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    typeSelect.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await settle()
    expect(typeSelect.textContent).toContain('UniFi Network')
    expect(document.activeElement).toBe(typeSelect)
    expect(schemeSelect.textContent).toContain('https')
    expect(schemeSelect.disabled).toBe(true)
    expect(host.querySelector<HTMLInputElement>('#service-create-port')?.value).toBe('443')
  } finally { app.unmount() }
})
