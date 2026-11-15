import { afterEach, expect, it, vi } from 'vitest'
import { clearToasts, dismissToast, toast } from '../../provider-sdk/portalkit/toast'

afterEach(() => clearToasts())

it('keeps a focused action available through a failure and a later successful retry', async () => {
  let reject!: (reason?: unknown) => void
  const first = new Promise<void>((_resolve, fail) => { reject = fail })
  const run = vi.fn().mockReturnValueOnce(first).mockResolvedValueOnce(undefined)
  const id = toast('info', 'Connection needs attention.', { label: 'Retry', run })
  const card = document.getElementById(`k-toast-${id}`)!
  const action = card.querySelector<HTMLButtonElement>('.k-toast__action')!
  action.focus(); action.click(); action.click()
  expect(run).toHaveBeenCalledTimes(1)
  expect(document.activeElement).toBe(action)
  expect(action.getAttribute('aria-disabled')).toBe('true')
  expect(action.getAttribute('aria-busy')).toBe('true')
  reject(new Error('sensitive provider details'))
  await vi.waitFor(() => expect(card.textContent).toContain('Action failed. Try again.'))
  expect(card.textContent).not.toContain('sensitive provider details')
  expect(document.activeElement).toBe(action)
  expect(action.hasAttribute('aria-disabled')).toBe(false)
  action.click()
  await vi.waitFor(() => expect(document.getElementById(`k-toast-${id}`)).toBeNull())
  expect(run).toHaveBeenCalledTimes(2)
})

it('does not recreate a dismissed toast when its pending action rejects', async () => {
  let reject!: (reason?: unknown) => void
  const pending = new Promise<void>((_resolve, fail) => { reject = fail })
  const id = toast('info', 'Working', { label: 'Retry', run: () => pending })
  document.querySelector<HTMLButtonElement>(`#k-toast-${id} .k-toast__action`)!.click()
  dismissToast(id)
  reject(new Error('late failure'))
  await Promise.resolve()
  await Promise.resolve()
  expect(document.getElementById(`k-toast-${id}`)).toBeNull()
})

it('returns focus to the origin when clearing a focused notification stack', () => {
  const origin = document.createElement('button')
  document.body.append(origin)
  try {
    origin.focus()
    const id = toast('error', 'First issue')
    toast('error', 'Second issue')
    document.querySelector<HTMLButtonElement>(`#k-toast-${id} .k-toast__dismiss`)!.focus()
    clearToasts()
    expect(document.getElementById('k-toasts')).toBeNull()
    expect(document.activeElement).toBe(origin)
  } finally { origin.remove() }
})

it('leaves focus alone when clearing notifications from elsewhere', () => {
  const origin = document.createElement('button')
  const elsewhere = document.createElement('button')
  document.body.append(origin, elsewhere)
  try {
    origin.focus()
    toast('error', 'Issue')
    elsewhere.focus()
    clearToasts()
    expect(document.activeElement).toBe(elsewhere)
  } finally { origin.remove(); elsewhere.remove() }
})

it('follows new persistent messages only when the reader is at the end and outside the stack', () => {
  toast('error', 'First')
  const host = document.getElementById('k-toasts')!
  Object.defineProperty(host, 'clientHeight', { configurable: true, value: 100 })
  Object.defineProperty(host, 'scrollHeight', { configurable: true, get: () => host.childElementCount * 100 })
  toast('error', 'Second')
  expect(host.scrollTop).toBe(200)
  // Simulate a user scrolling back to the first item.
  host.scrollTop = 0
  toast('error', 'Third')
  expect(host.scrollTop).toBe(0)
  // A focused notification must not be scrolled away, even at the end.
  host.scrollTop = 200
  host.querySelector<HTMLButtonElement>('.k-toast__dismiss')!.focus()
  toast('error', 'Fourth')
  expect(host.scrollTop).toBe(200)
})
