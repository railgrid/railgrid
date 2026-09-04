import { afterEach, describe, expect, it, vi } from 'vitest'
import { AppStore } from '../store'
import { AgentsElement } from '../element'
import { clearToasts, subscribeToasts, toast } from '../ui/toast'
import { settle } from './helpers'

async function flushToastLifecycle(): Promise<void> {
  // PortalKit removes cards synchronously, while the adapter observes those
  // removals through MutationObserver. Two microtasks cover jsdom's observer
  // delivery without depending on a real-time delay.
  await Promise.resolve()
  await Promise.resolve()
}

describe('Agents toast adapter lifecycle', () => {
  afterEach(() => {
    clearToasts()
  })

  it('synchronizes adapter state when a toast auto-dismisses', async () => {
    vi.useFakeTimers()
    try {
      const snapshots: Array<Array<{ id: number; message: string }>> = []
      const off = subscribeToasts((items) => snapshots.push(items.map(({ id, message }) => ({ id, message }))))
      const id = toast('ok', 'saved')

      expect(snapshots.at(-1)).toEqual([{ id, message: 'saved' }])
      await vi.advanceTimersByTimeAsync(4000)
      await flushToastLifecycle()

      expect(snapshots.at(-1)).toEqual([])
      expect(document.getElementById(`k-toast-${id}`)).toBeNull()
      off()
    } finally {
      vi.useRealTimers()
    }
  })

  it('drops action callbacks when an action dismisses its toast', async () => {
    const action = vi.fn()
    const snapshots: Array<Array<{ id: number; action?: () => void }>> = []
    const off = subscribeToasts((items) => snapshots.push(items.map((item) => ({ id: item.id, action: item.action?.run }))))
    const id = toast('info', 'open run', { label: 'View run', run: action })

    document.querySelector<HTMLButtonElement>(`#k-toast-${id} .k-toast__action`)?.click()
    await flushToastLifecycle()

    expect(action).toHaveBeenCalledOnce()
    expect(snapshots.at(-1)).toEqual([])
    off()
  })

  it('evicts the oldest visible toast without retaining its action', () => {
    const firstAction = vi.fn()
    const snapshots: Array<Array<{ id: number; action?: () => void }>> = []
    const off = subscribeToasts((items) => snapshots.push(items.map((item) => ({ id: item.id, action: item.action?.run }))))
    const first = toast('ok', 'first', { label: 'first action', run: firstAction })
    const second = toast('ok', 'second')
    const third = toast('ok', 'third')
    const fourth = toast('ok', 'fourth')

    expect(snapshots.at(-1)?.map((item) => item.id)).toEqual([second, third, fourth])
    expect(snapshots.at(-1)?.some((item) => item.id === first || item.action === firstAction)).toBe(false)
    off()
  })

  it('clears rendered and subscribed state together', () => {
    const snapshots: number[][] = []
    const off = subscribeToasts((items) => snapshots.push(items.map((item) => item.id)))
    toast('error', 'failed')

    clearToasts()

    expect(snapshots.at(-1)).toEqual([])
    expect(document.getElementById('k-toasts')).toBeNull()
    off()
  })

  it('keeps IDs isolated when two standalone bundles share the toast host', async () => {
    // Query imports force two independent Vite module instances, matching
    // two separately loaded provider bundles while retaining one global DOM.
    // @ts-expect-error Vite query imports intentionally create independent bundles.
    const bundleOne = await import('../portalkit/toast?bundle=one')
    // @ts-expect-error Vite query imports intentionally create independent bundles.
    const bundleTwo = await import('../portalkit/toast?bundle=two')
    document.getElementById('k-toasts')?.remove()
    bundleOne.clearToasts()
    bundleTwo.clearToasts()

    const first = bundleOne.toast('ok', 'bundle one')
    const second = bundleTwo.toast('ok', 'bundle two')
    expect(second).not.toBe(first)
    expect(document.getElementById(`k-toast-${first}`)).not.toBeNull()
    expect(document.getElementById(`k-toast-${second}`)).not.toBeNull()

    bundleOne.dismissToast(first)
    expect(document.getElementById(`k-toast-${first}`)).toBeNull()
    expect(document.getElementById(`k-toast-${second}`)).not.toBeNull()
    bundleTwo.dismissToast(second)
  })

  it('clears action-bearing state when the host switches tenants', async () => {
    const action = vi.fn()
    const snapshots: Array<Array<{ id: number; action?: () => void }>> = []
    const off = subscribeToasts((items) => snapshots.push(items.map((item) => ({ id: item.id, action: item.action?.run }))))
    toast('ok', 'workspace A', { label: 'View', run: action })

    const loadAll = vi.spyOn(AppStore.prototype, 'loadAll').mockImplementation(() => undefined)
    const connect = vi.spyOn(AppStore.prototype, 'connect').mockImplementation(() => undefined)
    const tag = 'test-agents-toast-lifecycle'
    if (!customElements.get(tag)) customElements.define(tag, AgentsElement)
    const element = document.createElement(tag) as AgentsElement
    document.body.appendChild(element)
    element.farosContext = { basePath: '/ui/providers/agents', orgUUID: 'org-a', workspaceUUID: 'ws-a' }
    await settle(element)
    element.farosContext = { basePath: '/ui/providers/agents', orgUUID: 'org-b', workspaceUUID: 'ws-b' }
    await settle(element)

    expect(snapshots.at(-1)).toEqual([])
    expect(document.getElementById('k-toasts')).toBeNull()
    expect(action).not.toHaveBeenCalled()
    loadAll.mockRestore()
    connect.mockRestore()
    off()
  })
})
