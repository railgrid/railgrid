// @vitest-environment happy-dom

import { createApp, nextTick, type App } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import DashboardTile from './DashboardTile.vue'
import { api } from './api'

vi.mock('./api', () => ({
  api: { listInstances: vi.fn() },
  isContextChangedError: vi.fn(() => false),
  setHostFetch: vi.fn(),
  setTenant: vi.fn(),
  setToken: vi.fn(),
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
    host.addEventListener('faros-navigate', event => navigate((event as CustomEvent).detail))

    app = createApp(DashboardTile, { context: { tenant: 'cluster-a', token: 'token-a' } })
    app.mount(host)
    await flush()

    host.querySelector<HTMLButtonElement>('.group')?.click()
    expect(navigate).toHaveBeenCalledWith({ path: 'instances/demo%20instance' })
  })
})
