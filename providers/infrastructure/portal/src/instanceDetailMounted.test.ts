// @vitest-environment happy-dom

import { createApp, nextTick, type App } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import InstanceDetailPage from './views/InstanceDetailPage.vue'
import { createResourceTombstones } from './refresh'
import type { Instance } from './types'
import { api } from './api'
import { confirmDialog } from './portalkit/confirm'

vi.mock('./api', () => ({
  api: {
    getInstance: vi.fn(),
    getTemplate: vi.fn(),
    deleteInstance: vi.fn(),
  },
  isContextChangedError: vi.fn(() => false),
}))

vi.mock('./portalkit/confirm', () => ({
  confirmDialog: vi.fn(),
}))

const instance: Instance = {
  uid: 'uid-instance-1',
  name: 'demo-instance',
  namespace: 'default',
  template: 'demo-template',
  phase: 'Ready',
  message: 'Ready snapshot',
  values: { snapshot: 'retained-value' },
  children: [],
  createdAt: '2026-08-25T00:00:00Z',
  generation: 1,
  observedGeneration: 1,
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

async function flush(): Promise<void> {
  await Promise.resolve()
  await nextTick()
  await new Promise<void>(resolve => window.setTimeout(resolve, 0))
  await nextTick()
}

async function flushTicks(): Promise<void> {
  for (let attempt = 0; attempt < 6; attempt += 1) {
    await Promise.resolve()
    await nextTick()
  }
}

function text(selector: string): string {
  return document.querySelector(selector)?.textContent?.replace(/\s+/g, ' ').trim() || ''
}

describe('mounted Infrastructure instance detail deletion behavior', () => {
  let app: App<Element> | null = null
  let host: HTMLDivElement

  beforeEach(() => {
    vi.mocked(api.getInstance).mockResolvedValue({ ...instance })
    vi.mocked(api.getTemplate).mockResolvedValue({ template: { view: null } as never })
    vi.mocked(confirmDialog).mockResolvedValue(true)
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

  it('keeps the successful snapshot and header actions stable during a background poll', async () => {
    vi.useFakeTimers()
    const backgroundRequest = deferred<Instance>()
    vi.mocked(api.getInstance)
      .mockResolvedValueOnce({ ...instance })
      .mockReturnValueOnce(backgroundRequest.promise)
    app = createApp(InstanceDetailPage, {
      instanceName: instance.name,
      tombstones: createResourceTombstones(),
    })
    app.mount(host)
    await flushTicks()

    const refresh = host.querySelector<HTMLButtonElement>('.instance-detail__actions > button')
    expect(refresh?.textContent).toContain('Refresh')
    expect(refresh?.disabled).toBe(false)
    expect(host.textContent).toContain('retained-value')

    vi.advanceTimersByTime(30_000)
    await flushTicks()

    expect(api.getInstance).toHaveBeenCalledTimes(2)
    expect(refresh?.textContent).toContain('Refresh')
    expect(refresh?.textContent).not.toContain('Refreshing')
    expect(refresh?.disabled).toBe(false)
    expect(host.textContent).toContain('retained-value')

    refresh?.click()
    await nextTick()
    expect(refresh?.textContent).toContain('Refreshing')
    expect(refresh?.disabled).toBe(true)

    backgroundRequest.resolve({ ...instance, message: 'Updated snapshot' })
    await flushTicks()
    expect(refresh?.textContent).toContain('Refresh')
    expect(refresh?.disabled).toBe(false)
    expect(host.textContent).toContain('retained-value')
  })

  it('omits empty template detail groups while retaining meaningful groups', async () => {
    vi.mocked(api.getTemplate).mockResolvedValue({
      template: {
        view: {
          detail: [
            { title: 'Access', fields: [{ label: 'Snapshot', path: 'spec.snapshot' }] },
            { title: 'Readiness', fields: [{ label: 'App', path: 'status.app' }] },
          ],
        },
      } as never,
    })
    app = createApp(InstanceDetailPage, {
      instanceName: instance.name,
      tombstones: createResourceTombstones(),
    })
    app.mount(host)
    await flush()

    const sections = Array.from(host.querySelectorAll<HTMLElement>('section[id^="instance-detail-group-"]'))
    expect(sections).toHaveLength(1)
    expect(sections[0].textContent).toContain('Access')
    expect(sections[0].textContent).toContain('Snapshot')
    expect(sections[0].textContent).toContain('retained-value')
    expect(host.textContent).not.toContain('Readiness')
    expect(host.textContent).not.toContain('App —')
  })

  it('retains the snapshot and recovers every action after a deferred delete fails', async () => {
    const deleteRequest = deferred<void>()
    vi.mocked(api.deleteInstance).mockReturnValue(deleteRequest.promise)
    const navigate = vi.fn()
    app = createApp(InstanceDetailPage, {
      instanceName: instance.name,
      tombstones: createResourceTombstones(),
      onNavigate: navigate,
    })
    app.mount(host)
    await flush()

    expect(text('.k-resource-page__status .k-badge')).toContain('Ready')
    expect(host.textContent).toContain('retained-value')
    const actionMenu = host.querySelector<HTMLElement>('.k-action-menu')
    const actionTrigger = host.querySelector<HTMLButtonElement>('.k-action-menu__trigger')
    const back = host.querySelector<HTMLAnchorElement>('.instance-detail__back')
    const refresh = host.querySelector<HTMLButtonElement>('.instance-detail__actions > button')
    expect(actionMenu).not.toBeNull()
    expect(actionTrigger).not.toBeNull()
    expect(back).not.toBeNull()
    expect(refresh).not.toBeNull()

    actionTrigger!.click()
    await flush()
    const deleteButton = host.querySelector<HTMLButtonElement>('.k-action-menu__item')
    expect(deleteButton).not.toBeNull()
    deleteButton!.click()
    await flush()

    expect(host.querySelector('.k-action-menu__item')).toBeNull()
    expect(text('.k-resource-page__status .k-badge')).toContain('Deleting')
    expect(host.querySelector('[role="status"][aria-live="polite"].instance-message')?.textContent)
      .toContain('Deleting this instance.')
    expect(host.textContent).toContain('retained-value')
    expect(refresh!.disabled).toBe(true)
    expect(back!.getAttribute('aria-disabled')).toBe('true')
    back!.click()
    refresh!.click()
    expect(navigate).not.toHaveBeenCalled()
    expect(api.getInstance).toHaveBeenCalledTimes(1)

    deleteRequest.reject({ reason: 'HTTPError', message: 'delete failed' })
    await flush()

    expect(host.querySelector('[role="alert"][aria-live="assertive"]')?.textContent)
      .toContain('HTTPError: delete failed')
    expect(host.querySelector('[role="status"][aria-live="polite"].instance-message')).toBeNull()
    expect(text('.k-resource-page__status .k-badge')).toContain('Ready')
    expect(host.textContent).toContain('retained-value')
    expect(refresh!.disabled).toBe(false)
    expect(back!.getAttribute('aria-disabled')).toBeNull()
    back!.click()
    expect(navigate).toHaveBeenCalledWith('instances')
  })
})
