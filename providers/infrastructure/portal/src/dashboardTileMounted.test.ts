// @vitest-environment happy-dom

import { createApp, h, nextTick, ref, type App } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import DashboardTile from './DashboardTile.vue'
import { api } from './api'
import type { TileContext } from './portalkit/dashboardtile'

vi.mock('./api', () => ({
  api: { listInstances: vi.fn() },
  isContextChangedError: vi.fn(() => false),
  setHostFetch: vi.fn(),
  setTenant: vi.fn(),
}))

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  const promise = new Promise<T>(resolvePromise => { resolve = resolvePromise })
  return { promise, resolve }
}

async function flush(): Promise<void> {
  await Promise.resolve()
  await nextTick()
  await Promise.resolve()
  await nextTick()
}

describe('Infrastructure dashboard tile refresh lifecycle', () => {
  let app: App<Element> | null = null
  let host: HTMLDivElement

  beforeEach(() => {
    vi.useFakeTimers()
    host = document.createElement('div')
    document.body.appendChild(host)
  })

  afterEach(() => {
    app?.unmount()
    app = null
    host.remove()
    vi.clearAllMocks()
    vi.useRealTimers()
  })

  it('queues a timer tick behind a slow read and cleans it up on unmount', async () => {
    const first = deferred<{ items: never[]; identities: never[] }>()
    vi.mocked(api.listInstances)
      .mockReturnValueOnce(first.promise)
      .mockResolvedValueOnce({ items: [], identities: [] })

    app = createApp(DashboardTile, { context: { tenant: 'cluster-a', token: 'token-a' } })
    app.mount(host)
    await flush()
    expect(api.listInstances).toHaveBeenCalledTimes(1)

    vi.advanceTimersByTime(30_000)
    await flush()
    expect(api.listInstances).toHaveBeenCalledTimes(1)

    first.resolve({ items: [], identities: [] })
    await flush()
    expect(api.listInstances).toHaveBeenCalledTimes(2)

    app.unmount()
    vi.advanceTimersByTime(60_000)
    await flush()
    expect(api.listInstances).toHaveBeenCalledTimes(2)
  })

  it('bubbles instance navigation through the shared dispatcher', async () => {
    vi.mocked(api.listInstances).mockResolvedValue({
      items: [{
        name: 'demo instance',
        uid: 'uid-1',
        namespace: 'default',
        template: 'demo',
        phase: 'Ready',
        createdAt: '2026-09-03T00:00:00Z',
      }],
      identities: [{ name: 'demo instance', uid: 'uid-1' }],
    })
    const navigate = vi.fn()
    host.addEventListener('railgrid-navigate', event => navigate((event as CustomEvent).detail))

    app = createApp(DashboardTile, { context: { tenant: 'cluster-a', token: 'token-a' } })
    app.mount(host)
    await flush()

    host.querySelector<HTMLButtonElement>('.group')?.click()
    expect(navigate).toHaveBeenCalledWith({ path: 'instances/demo%20instance' })
  })

  it('keeps a snapshot across theme and token pushes but fences same-workspace account changes', async () => {
    const oldRead = deferred<{ items: never[]; identities: never[] }>()
    const newRead = deferred<{ items: never[]; identities: never[] }>()
    const context = ref<TileContext & { theme?: string }>({
      tenant: 'cluster-a', user: { userId: 'first' }, fetch: async () => new Response('{}'), token: 'old',
    })
    vi.mocked(api.listInstances)
      .mockResolvedValueOnce({ items: [], identities: [] })
      .mockReturnValueOnce(oldRead.promise)
      .mockReturnValueOnce(newRead.promise)
    app = createApp({ render: () => h(DashboardTile, { context: context.value }) })
    app.mount(host)
    await flush()
    expect(api.listInstances).toHaveBeenCalledTimes(1)
    expect(host.textContent).toContain('No instances yet')

    context.value = { ...context.value, token: 'rotated', theme: 'dark' }
    await flush()
    expect(api.listInstances).toHaveBeenCalledTimes(1)
    expect(host.textContent).toContain('No instances yet')

    vi.advanceTimersByTime(30_000)
    await flush()
    expect(api.listInstances).toHaveBeenCalledTimes(2)
    context.value = { ...context.value, user: { userId: 'second' } }
    await flush()
    expect(host.textContent).not.toContain('No instances yet')
    oldRead.resolve({ items: [], identities: [] })
    await flush()
    expect(api.listInstances).toHaveBeenCalledTimes(3)
    expect(host.textContent).not.toContain('No instances yet')
    newRead.resolve({ items: [], identities: [] })
    await flush()
    expect(host.textContent).toContain('No instances yet')
  })
})
