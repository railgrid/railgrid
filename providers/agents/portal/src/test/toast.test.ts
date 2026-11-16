import { createApp, nextTick } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import ToastHost from '../portalkit/ToastHost.vue'
import { toast as sharedToast, clearToasts as clearShared } from '../portalkit/toast'
import { clearToasts, dismissToast, subscribeToasts, toast } from '../ui/toast'

let host: HTMLDivElement
let app: ReturnType<typeof createApp>
beforeEach(() => {
  host = document.createElement('div')
  document.body.append(host)
  app = createApp(ToastHost, { owner: 'fallback' })
  app.mount(host)
})
afterEach(() => { clearToasts(); clearShared('external'); app.unmount(); host.remove(); vi.useRealTimers() })

it('queues provider notices behind a higher-priority host error and isolates clear', async () => {
  const snapshots: string[][] = []
  const off = subscribeToasts(items => snapshots.push(items.map(item => item.message)))
  sharedToast('error', 'host failure', { scope: 'external' })
  const id = toast('ok', 'agent saved')
  await nextTick()
  expect(document.querySelector('.k-toast__message')?.textContent).toContain('host failure')
  expect(snapshots.at(-1)).toEqual(['agent saved'])
  clearToasts()
  expect(snapshots.at(-1)).toEqual([])
  expect(document.querySelector('.k-toast__message')?.textContent).toContain('host failure')
  dismissToast(id)
  off()
})

it('removes action callbacks and reports timeout from the shared host', async () => {
  vi.useFakeTimers()
  const snapshots: number[][] = []
  const off = subscribeToasts(items => snapshots.push(items.map(item => item.id)))
  toast('ok', 'saved')
  await nextTick()
  await vi.advanceTimersByTimeAsync(5000)
  await nextTick()
  expect(snapshots.at(-1)).toEqual([])
  const action = vi.fn()
  toast('info', 'open', { label: 'Open', run: action })
  await nextTick()
  await vi.advanceTimersByTimeAsync(500)
  document.querySelector<HTMLButtonElement>('.k-toast__action')!.click()
  await Promise.resolve()
  await nextTick()
  expect(action).toHaveBeenCalledOnce()
  expect(snapshots.at(-1)).toEqual([])
  off()
})

it('keeps a failed action available without leaking its thrown details', async () => {
  const action = vi.fn().mockRejectedValueOnce(new Error('sensitive details')).mockResolvedValueOnce(undefined)
  toast('error', 'Needs attention', { label: 'Retry', run: action })
  await nextTick()
  const button = document.querySelector<HTMLButtonElement>('.k-toast__action')!
  button.focus()
  button.click()
  await vi.waitFor(() => expect(document.body.textContent).toContain('Action failed. Try again.'))
  expect(document.body.textContent).not.toContain('sensitive details')
  expect(document.activeElement).toBe(button)
  button.click()
  await vi.waitFor(() => expect(document.querySelector('.k-toast__action')).toBeNull())
  expect(action).toHaveBeenCalledTimes(2)
})

it('isolates independent adapter instances while sharing one queue', async () => {
  // @ts-expect-error Vite query imports simulate separate bundles.
  const other = await import('../ui/toast?other-bundle')
  const first = toast('error', 'first bundle')
  const second = other.toast('ok', 'second bundle')
  const snapshots: number[][] = []
  const stop = other.subscribeToasts((items: Array<{ id: number }>) => snapshots.push(items.map(item => item.id)))
  await nextTick()
  other.dismissToast(first)
  expect(document.querySelector('.k-toast__message')?.textContent).toContain('first bundle')
  clearToasts()
  await nextTick()
  expect(snapshots.at(-1)).toEqual([second])
  expect(document.querySelector('.k-toast__message')?.textContent).toContain('second bundle')
  other.clearToasts()
  expect(snapshots.at(-1)).toEqual([])
  stop()
})
