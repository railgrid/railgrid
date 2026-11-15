// @vitest-environment happy-dom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import ServiceCreate from './ServiceCreate.vue'
import { setHostFetch, setTenant, setToken } from './api'
vi.mock('./portalkit/toast', () => ({ toast: vi.fn() }))
async function flush() { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
beforeEach(() => {
  const values = new Map<string, string>()
  vi.stubGlobal('localStorage', { getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => values.set(key, value), removeItem: (key: string) => values.delete(key), clear: () => values.clear(), key: (index: number) => [...values.keys()][index] ?? null, get length() { return values.size } })
})
afterEach(() => { setHostFetch(null); setTenant(null); setToken(null); localStorage.clear() })
it('renders a field error for a Kubernetes AlreadyExists response through the real API', async () => {
  setTenant('workspace')
  setHostFetch(async (input, init) => {
    const path = String(input)
    if (init?.method === 'POST') return new Response(JSON.stringify({ kind: 'Status', apiVersion: 'v1', status: 'Failure', reason: 'AlreadyExists', message: 'service already exists', code: 409 }), { status: 409 })
    const items = path.includes('linuxservers') ? [{ metadata: { name: 'edge-a' }, status: { connected: true } }] : []
    return new Response(JSON.stringify({ items }), { status: 200 })
  })
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp(ServiceCreate)
  try {
    app.mount(host)
    await flush()
    const name = host.querySelector<HTMLInputElement>('input[placeholder="home-assistant"]')!
    name.value = 'living-room'
    name.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    const button = host.querySelector<HTMLButtonElement>('button[type="submit"]')!
    button.focus()
    button.click()
    await flush()
    expect(name.getAttribute('aria-invalid')).toBe('true')
    expect(host.querySelector('#service-create-name-error')?.textContent).toContain('already exists')
    expect(document.activeElement).toBe(name)
  } finally { app.unmount(); host.remove() }
})

it('keeps a lost create response uncertain through the real API', async () => {
  setTenant('workspace')
  let posts = 0
  setHostFetch(async (input, init) => {
    const path = String(input)
    if (init?.method === 'POST') { posts++; throw new TypeError('connection lost') }
    if (path.endsWith('/services/living-room')) return new Response(JSON.stringify({ kind: 'Status', status: 'Failure', reason: 'NotFound', message: 'not found', details: { name: 'living-room', group: 'edges.railgrid.ai' }, code: 404 }), { status: 404 })
    const items = path.includes('linuxservers') ? [{ metadata: { name: 'edge-a' }, status: { connected: true } }] : []
    return new Response(JSON.stringify({ items }), { status: 200 })
  })
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp(ServiceCreate)
  try {
    app.mount(host)
    await flush()
    const name = host.querySelector<HTMLInputElement>('input[placeholder="home-assistant"]')!
    name.value = 'living-room'
    name.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flush()
    expect(host.textContent).toContain('Could not confirm whether service “living-room”')
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    expect(posts).toBe(1)
    ;[...host.querySelectorAll('button')].find(button => button.textContent?.includes('Check service'))!.click()
    await flush()
    expect(host.textContent).toContain('original request may still be processing')
  } finally { app.unmount(); host.remove() }
})

it('keeps the outcome uncertain when the token changes during creation', async () => {
  setTenant('workspace')
  setToken('old-token')
  let finish!: (response: Response) => void
  let posts = 0
  setHostFetch(async (input, init) => {
    if (init?.method === 'POST') {
      posts++
      return new Promise<Response>(resolve => { finish = resolve })
    }
    const items = String(input).includes('linuxservers') ? [{ metadata: { name: 'edge-a' }, status: { connected: true } }] : []
    return new Response(JSON.stringify({ items }), { status: 200 })
  })
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp(ServiceCreate)
  try {
    app.mount(host)
    await flush()
    const name = host.querySelector<HTMLInputElement>('input[placeholder="home-assistant"]')!
    name.value = 'living-room'
    name.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flush()
    expect(posts).toBe(1)
    setToken('new-token')
    finish(new Response(JSON.stringify({ apiVersion: 'edges.railgrid.ai/v1alpha1', kind: 'Service', metadata: { name: 'living-room' } }), { status: 201 }))
    await flush()
    expect(host.textContent).toContain('Could not confirm whether service “living-room”')
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    expect(posts).toBe(1)
  } finally { app.unmount(); host.remove() }
})

it('restores a pending request after remount without accepting a late completion', async () => {
  setTenant('workspace')
  let finish!: (response: Response) => void
  setHostFetch(async (input, init) => {
    if (init?.method === 'POST') return new Promise<Response>(resolve => { finish = resolve })
    const items = String(input).includes('linuxservers') ? [{ metadata: { name: 'edge-a' }, status: { connected: true } }] : []
    return new Response(JSON.stringify({ items }), { status: 200 })
  })
  const host = document.createElement('div')
  document.body.appendChild(host)
  let app = createApp(ServiceCreate, { cluster: 'workspace', userKey: 'user' })
  try {
    app.mount(host)
    await flush()
    const name = host.querySelector<HTMLInputElement>('input[placeholder="home-assistant"]')!
    name.value = 'living-room'
    name.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flush()
    app.unmount()
    app = createApp(ServiceCreate, { cluster: 'workspace', userKey: 'user' })
    app.mount(host)
    await flush()
    expect(host.textContent).toContain('Could not confirm whether service “living-room”')
    finish(new Response(JSON.stringify({ apiVersion: 'edges.railgrid.ai/v1alpha1', kind: 'Service', metadata: { name: 'living-room' } }), { status: 201 }))
    await flush()
    expect(host.textContent).toContain('Could not confirm whether service “living-room”')
    expect(localStorage.length).toBe(1)
  } finally { app.unmount(); host.remove() }
})

it('creates a Kubernetes host target with explicit loopback when the host is blank', async () => {
  setTenant('workspace')
  let submitted: any
  setHostFetch(async (input, init) => {
    if (init?.method === 'POST') {
      submitted = JSON.parse(String(init.body))
      return new Response(JSON.stringify(submitted), { status: 201 })
    }
    const items = String(input).includes('kubernetesclusters') ? [{ metadata: { name: 'cluster-a' }, status: { connected: true } }] : []
    return new Response(JSON.stringify({ items }), { status: 200 })
  })
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp(ServiceCreate)
  try {
    app.mount(host)
    await flush()
    const name = host.querySelector<HTMLInputElement>('input[placeholder="home-assistant"]')!
    name.value = 'local-service'
    name.dispatchEvent(new Event('input', { bubbles: true }))
    const radio = host.querySelector<HTMLInputElement>('input[name="service-create-target-mode"][value="host"]')!
    radio.checked = true
    radio.dispatchEvent(new Event('change', { bubbles: true }))
    await nextTick()
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flush()
    expect(submitted.spec).toMatchObject({ edgeRef: { kind: 'KubernetesCluster', name: 'cluster-a' }, host: '127.0.0.1' })
    expect(submitted.spec.targetRef).toBeUndefined()
  } finally { app.unmount(); host.remove() }
})

it('patches a Kubernetes service to explicit loopback when switching to a blank host', async () => {
  const { default: ServiceEdit } = await import('./ServiceEdit.vue')
  setTenant('workspace')
  let patch: any
  const resource = { apiVersion: 'edges.railgrid.ai/v1alpha1', kind: 'Service', metadata: { name: 'svc' }, spec: { edgeRef: { kind: 'KubernetesCluster', name: 'cluster-a' }, targetRef: { name: 'backend', namespace: 'default' }, type: 'generic', scheme: 'http', port: 80 } }
  setHostFetch(async (_input, init) => {
    if (init?.method === 'PATCH') patch = JSON.parse(String(init.body))
    return new Response(JSON.stringify(resource), { status: 200 })
  })
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp(ServiceEdit, { serviceName: 'svc', catalog: [], edges: [] })
  try {
    app.mount(host)
    await flush()
    const radio = host.querySelector<HTMLInputElement>('input[name="service-edit-target-mode"][value="host"]')!
    radio.checked = true
    radio.dispatchEvent(new Event('change', { bubbles: true }))
    await nextTick()
    ;[...host.querySelectorAll('button')].find(button => button.textContent?.includes('Save configuration'))!.click()
    await flush()
    expect(patch.spec).toMatchObject({ host: '127.0.0.1', targetRef: null })
  } finally { app.unmount(); host.remove() }
})

it('requires a LAN host when editing a service whose type requires one', async () => {
  const { default: ServiceEdit } = await import('./ServiceEdit.vue')
  setTenant('workspace')
  let patches = 0
  const resource = { apiVersion: 'edges.railgrid.ai/v1alpha1', kind: 'Service', metadata: { name: 'svc' }, spec: { edgeRef: { kind: 'KubernetesCluster', name: 'cluster-a' }, host: '192.168.1.1', type: 'unifi', scheme: 'https', port: 443 } }
  setHostFetch(async (_input, init) => {
    if (init?.method === 'PATCH') patches++
    return new Response(JSON.stringify(resource), { status: 200 })
  })
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp(ServiceEdit, { serviceName: 'svc', catalog: [{ type: 'unifi', displayName: 'UniFi', hostRequired: true, hostHelp: 'LAN address', auth: 'none', credential: { fields: [] } }], edges: [] })
  try {
    app.mount(host)
    await flush()
    const input = host.querySelector<HTMLInputElement>('#service-edit-host')!
    input.value = '  '
    input.dispatchEvent(new Event('input', { bubbles: true }))
    ;[...host.querySelectorAll('button')].find(button => button.textContent?.includes('Save configuration'))!.click()
    await flush()
    expect(patches).toBe(0)
    expect(document.activeElement).toBe(input)
    expect(input.getAttribute('aria-invalid')).toBe('true')
    expect(input.getAttribute('aria-describedby')).toContain('service-edit-host-error')
    expect(input.getAttribute('aria-describedby')).toContain('service-edit-host-help')
    expect(host.querySelector('#service-edit-host-error')?.textContent).toContain('Enter a host')
    expect(host.querySelector<HTMLInputElement>('input[name="service-edit-target-mode"][value="kube"]')?.disabled).toBe(true)
    input.value = '192.168.1.2'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(input.getAttribute('aria-invalid')).toBeNull()
    ;[...host.querySelectorAll('button')].find(button => button.textContent?.includes('Save configuration'))!.click()
    await flush()
    expect(patches).toBe(1)
  } finally { app.unmount(); host.remove() }
})

it.each([
  { kind: 'KubernetesCluster', otherType: 'server', disabled: false },
  { kind: 'LinuxServer', otherType: 'kubernetes', disabled: true },
])('uses the service edge kind when a different kind has the same name: $kind', async ({ kind, otherType, disabled }) => {
  const { default: ServiceEdit } = await import('./ServiceEdit.vue')
  setTenant('workspace')
  let patch: any
  const resource = { apiVersion: 'edges.railgrid.ai/v1alpha1', kind: 'Service', metadata: { name: 'svc' }, spec: { edgeRef: { kind, name: 'shared' }, type: 'generic', scheme: 'http', port: 80 } }
  setHostFetch(async (_input, init) => {
    if (init?.method === 'PATCH') patch = JSON.parse(String(init.body))
    return new Response(JSON.stringify(resource), { status: 200 })
  })
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp(ServiceEdit, { serviceName: 'svc', catalog: [], edges: [{ name: 'shared', type: otherType }] as any })
  try {
    app.mount(host)
    await flush()
    expect(host.querySelector<HTMLInputElement>('input[name="service-edit-target-mode"][value="kube"]')!.disabled).toBe(disabled)
    ;[...host.querySelectorAll('button')].find(button => button.textContent?.includes('Save configuration'))!.click()
    await flush()
    expect(patch.spec.host).toBe(disabled ? '' : '127.0.0.1')
  } finally { app.unmount(); host.remove() }
})
