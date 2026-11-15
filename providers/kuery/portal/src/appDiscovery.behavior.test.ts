// @vitest-environment happy-dom
import { createApp, nextTick, reactive } from 'vue'
import { expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({ inventory: vi.fn(), ensure: vi.fn() }))
vi.mock('./savedviews', () => ({
  kubeClientFor: (request: { fetch: unknown }) => ({ fetch: request.fetch }),
  playgroundViewName: async () => 'view', ensurePlaygroundView: mocks.ensure,
  listEdgeInventory: mocks.inventory,
}))
vi.mock('./components/TopologyView.vue', () => ({ default: { props: ['edges'], template: '<div>Topology fixture: {{ edges.join(", ") }}</div>' } }))
vi.mock('./components/InventoryView.vue', () => ({ default: { template: '<div />' } }))
vi.mock('./components/PlaygroundView.vue', () => ({ default: { template: '<textarea aria-label="Query draft" />' } }))
vi.mock('./components/ImpactView.vue', () => ({ default: { template: '<div />' } }))
import App from './App.vue'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(accept => { resolve = accept })
  return { promise, resolve }
}
async function flush() { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }

it('refreshes discovered edges while retaining the last snapshot on failure', async () => {
  const pending = deferred<{ connected: string[]; total: number }>()
  mocks.inventory.mockReset().mockResolvedValueOnce({ connected: ['old-edge'], total: 1 })
    .mockRejectedValueOnce(new Error('offline')).mockReturnValueOnce(pending.promise)
  mocks.ensure.mockReset().mockResolvedValue(undefined)
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(App, { state: reactive({ context: { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token', fetch: vi.fn() } }) })
  try {
    app.mount(host); await flush()
    const refresh = [...host.querySelectorAll('button')].find(button => button.textContent?.includes('Refresh edges'))!
    refresh.focus(); refresh.click(); await flush()
    expect(host.textContent).toContain('Could not refresh workspace setup')
    expect(host.textContent).toContain('old-edge')
    expect(document.activeElement).toBe(refresh)
    refresh.click(); await flush()
    expect(refresh.textContent).toContain('Refreshing edges')
    refresh.click(); expect(mocks.inventory).toHaveBeenCalledTimes(3)
    pending.resolve({ connected: ['new-edge', 'old-edge'], total: 2 }); await flush()
    expect(host.textContent).toContain('2 connected Kubernetes edges')
    expect(host.textContent).toContain('new-edge, old-edge')
    expect(host.textContent).not.toContain('Could not refresh workspace setup')
    expect(document.activeElement).toBe(refresh)
  } finally { app.unmount(); host.remove() }
})

it('shows first-run guidance without provisioning a query view when no edges are connected', async () => {
  const originalPath = window.location.href
  window.history.replaceState({}, '', '/ui/11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222/providers/kuery')
  mocks.inventory.mockReset().mockResolvedValue({ connected: [], total: 0 })
  mocks.ensure.mockReset().mockRejectedValue(new Error('forbidden'))
  const state = reactive({ context: { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token', fetch: vi.fn() } })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(App, { state })
  try {
    app.mount(host); await flush()
    expect(host.textContent).toContain('Connect your first Kubernetes edge')
    expect(host.querySelector('a')?.getAttribute('href')).toBe('/ui/11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222/providers/edges/connect/edge/kubernetes')
    expect(host.querySelector('a')?.getAttribute('target')).toBe('_blank')
    expect(host.querySelector('a')?.getAttribute('rel')).toBe('noopener noreferrer')
    expect(host.textContent).toContain('return here and select Check again')
    expect(host.querySelector('a')?.textContent).toContain('opens in a new tab')
    expect(mocks.ensure).not.toHaveBeenCalled()
  } finally { app.unmount(); host.remove(); window.history.replaceState({}, '', originalPath) }
})

it('links to the scoped edges list when existing Kubernetes edges are disconnected', async () => {
  const originalPath = window.location.href
  window.history.replaceState({}, '', '/ui/11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222/providers/kuery')
  mocks.inventory.mockReset().mockResolvedValue({ connected: [], total: 1 })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(App, { state: reactive({ context: { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token', fetch: vi.fn() } }) })
  try {
    app.mount(host); await flush()
    expect(host.querySelector('a')?.getAttribute('href')).toBe('/ui/11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222/providers/edges')
    expect(host.querySelector('a')?.getAttribute('target')).toBe('_blank')
    expect(host.querySelector('a')?.getAttribute('rel')).toBe('noopener noreferrer')
    expect(host.textContent).toContain('return here and select Check again')
    expect(host.querySelector('a')?.textContent).toContain('opens in a new tab')
  } finally { app.unmount(); host.remove(); window.history.replaceState({}, '', originalPath) }
})

it('reports and retries query setup without losing discovered edges', async () => {
  mocks.inventory.mockReset().mockResolvedValue({ connected: ['edge'], total: 1 })
  mocks.ensure.mockReset().mockRejectedValueOnce(new Error('forbidden')).mockResolvedValueOnce(undefined)
  const state = reactive({ context: { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token', fetch: vi.fn() } })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(App, { state })
  try {
    app.mount(host); await flush()
    expect(host.textContent).toContain('1 connected Kubernetes edge')
    expect(host.textContent).toContain('Could not prepare queries')
    expect(host.textContent).toContain('forbidden')
    const retry = Array.from(host.querySelectorAll('button')).find(button => button.textContent?.includes('Retry'))!
    retry.click(); await flush()
    expect(host.textContent).toContain('Topology fixture')
    expect(mocks.ensure).toHaveBeenCalledTimes(2)
  } finally { app.unmount(); host.remove() }
})

it('offers one retry when query setup and a subsequent discovery both fail', async () => {
  mocks.inventory.mockReset().mockResolvedValueOnce({ connected: ['edge'], total: 1 }).mockRejectedValueOnce(new Error('offline'))
  mocks.ensure.mockReset().mockRejectedValue(new Error('forbidden'))
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(App, { state: reactive({ context: { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token', fetch: vi.fn() } }) })
  try {
    app.mount(host); await flush()
    const retry = [...host.querySelectorAll('button')].find(button => button.textContent?.trim() === 'Retry')!
    retry.click(); await flush()
    expect(host.textContent).toContain('Could not refresh workspace setup')
    expect(host.textContent).toContain('Could not prepare queries')
    expect([...host.querySelectorAll('button')].filter(button => button.textContent?.trim() === 'Retry')).toEqual([retry])
  } finally { app.unmount(); host.remove() }
})

it('ignores a late query setup failure after switching workspaces', async () => {
  const pending = deferred<void>()
  mocks.inventory.mockReset().mockResolvedValue({ connected: ['edge'], total: 1 })
  mocks.ensure.mockReset().mockReturnValueOnce(pending.promise).mockResolvedValue(undefined)
  const state = reactive({ context: { basePath: '/ui/providers/kuery', tenant: 'old', token: 'token', fetch: vi.fn() } })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(App, { state })
  try {
    app.mount(host); await flush()
    state.context.tenant = 'new'; await flush()
    expect(host.textContent).toContain('Topology fixture')
    pending.resolve(); await flush()
    expect(host.textContent).toContain('Topology fixture')
    expect(mocks.ensure).toHaveBeenCalledTimes(2)
  } finally { app.unmount(); host.remove() }
})

it('rechecks setup when host transport arrives and fences the prior transport', async () => {
  const oldRead = deferred<{ connected: string[]; total: number }>()
  const oldFetch = vi.fn(), newFetch = vi.fn()
  mocks.ensure.mockReset().mockResolvedValue(undefined)
  mocks.inventory.mockReset().mockImplementation((kube: { fetch: unknown }) => kube.fetch === oldFetch
    ? oldRead.promise : Promise.resolve({ connected: ['new-edge'], total: 1 }))
  const state = reactive({ context: { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token', fetch: oldFetch } })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(App, { state })
  try {
    app.mount(host)
    await flush()
    state.context.fetch = newFetch
    await flush()
    expect(mocks.inventory).toHaveBeenCalledTimes(2)
    expect(host.textContent).toContain('1 connected Kubernetes edge')
    oldRead.resolve({ connected: [], total: 0 })
    await flush()
    expect(host.textContent).toContain('1 connected Kubernetes edge')
    expect(host.textContent).not.toContain('Connect your first Kubernetes edge')
    expect(mocks.ensure).toHaveBeenCalledTimes(1)
  } finally {
    app.unmount()
    host.remove()
  }
})

it('keeps retry focused during a failed read, guards duplicates, and moves focus on success', async () => {
  const failed = deferred<{ connected: string[]; total: number }>()
  const succeeded = deferred<{ connected: string[]; total: number }>()
  mocks.inventory.mockReset().mockRejectedValueOnce(new Error('offline')).mockReturnValueOnce(failed.promise).mockReturnValueOnce(succeeded.promise)
  mocks.ensure.mockReset().mockResolvedValue(undefined)
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(App, { state: reactive({ context: { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token', fetch: vi.fn() } }) })
  try {
    app.mount(host); await flush()
    const retry = Array.from(host.querySelectorAll('button')).find(button => button.textContent?.includes('Retry'))!
    retry.focus(); retry.click(); await flush()
    expect(document.activeElement).toBe(retry)
    expect(retry.textContent).toContain('Retrying')
    expect(retry.getAttribute('aria-disabled')).toBe('true')
    retry.click(); expect(mocks.inventory).toHaveBeenCalledTimes(2)
    // Reject the pending read while the same control remains focused.
    // A rejected promise is provided by resolving to a throwing thenable.
    failed.resolve(Promise.reject(new Error('still offline')) as never); await flush()
    expect(document.activeElement).toBe(retry)
    expect(host.textContent).toContain('still offline')
    retry.click(); await flush()
    succeeded.resolve({ connected: [], total: 0 }); await flush()
    expect(host.textContent).toContain('Connect your first Kubernetes edge')
    expect(host.contains(document.activeElement)).toBe(true)
    expect(document.activeElement).not.toBe(retry)
  } finally { app.unmount(); host.remove() }
})

it('guards first-run checks and does not steal focus when the user moves away', async () => {
  const pending = deferred<{ connected: string[]; total: number }>()
  mocks.inventory.mockReset().mockResolvedValueOnce({ connected: [], total: 0 }).mockReturnValueOnce(pending.promise)
  mocks.ensure.mockReset().mockResolvedValue(undefined)
  const host = document.createElement('div'), outside = document.createElement('button')
  document.body.append(host, outside)
  const app = createApp(App, { state: reactive({ context: { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token', fetch: vi.fn() } }) })
  try {
    app.mount(host); await flush()
    const check = Array.from(host.querySelectorAll('button')).find(button => button.textContent?.includes('Check again'))!
    check.focus(); check.click(); await flush()
    expect(document.activeElement).toBe(check)
    expect(check.textContent).toContain('Checking')
    check.click(); expect(mocks.inventory).toHaveBeenCalledTimes(2)
    outside.focus()
    pending.resolve({ connected: ['edge'], total: 1 }); await flush()
    expect(host.textContent).toContain('Topology fixture')
    expect(document.activeElement).toBe(outside)
  } finally { app.unmount(); host.remove(); outside.remove() }
})


it('preserves a draft through a disconnect but discards it on workspace change', async () => {
  mocks.inventory.mockReset().mockResolvedValueOnce({ connected: ['edge'], total: 1 })
    .mockResolvedValueOnce({ connected: [], total: 1 })
    .mockResolvedValueOnce({ connected: ['edge'], total: 1 })
    .mockResolvedValue({ connected: ['other-edge'], total: 1 })
  mocks.ensure.mockReset().mockResolvedValue(undefined)
  const state = reactive({ context: { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token', fetch: vi.fn() } })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(App, { state })
  try {
    app.mount(host); await flush()
    ;[...host.querySelectorAll('button')].find(button => button.textContent?.includes('Playground'))!.click(); await flush()
    const draft = host.querySelector<HTMLTextAreaElement>('textarea')!
    draft.value = 'my query draft'
    ;[...host.querySelectorAll('button')].find(button => button.textContent?.includes('Refresh edges'))!.click(); await flush()
    expect(host.textContent).toContain('No Kubernetes edges are connected')
    expect(draft.closest('[style*="display: none"]')).not.toBeNull()
    ;[...host.querySelectorAll('button')].find(button => button.textContent?.includes('Check again'))!.click(); await flush()
    expect(host.querySelector('textarea')).toBe(draft)
    expect(draft.value).toBe('my query draft')
    state.context.tenant = 'other-cluster'; await flush()
    expect(host.querySelector('textarea')).not.toBe(draft)
    expect(host.querySelector('textarea')!.value).toBe('')
  } finally { app.unmount(); host.remove() }
})
