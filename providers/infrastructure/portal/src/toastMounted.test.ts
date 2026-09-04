// @vitest-environment happy-dom

import { createApp, nextTick, type App } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'

import InlineNotification from './portalkit/InlineNotification.vue'
import ToastHost from './portalkit/ToastHost.vue'
import {
  clearToasts,
  dismissToast,
  toast,
  type ToastHostRole,
  type ToastId,
  type ToastInput,
} from './portalkit/toast'

interface MountedApp {
  app: App<Element>
  host: HTMLDivElement
}

const mountedApps: MountedApp[] = []
const testScopes: string[] = []
let scopeSequence = 0

async function flush(): Promise<void> {
  for (let attempt = 0; attempt < 6; attempt += 1) {
    await Promise.resolve()
    await nextTick()
  }
}

async function settleToastLeave(): Promise<void> {
  await new Promise<void>(resolve => window.setTimeout(resolve, 200))
  await flush()
}

function newScope(label: string): string {
  const scope = `mounted-toast-${label}-${++scopeSequence}`
  testScopes.push(scope)
  return scope
}

function mountComponent(component: Parameters<typeof createApp>[0], props: Record<string, unknown> = {}): MountedApp {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp(component, props)
  app.mount(host)
  const mounted = { app, host }
  mountedApps.push(mounted)
  return mounted
}

function mountToast(owner: ToastHostRole = 'primary'): MountedApp {
  return mountComponent(ToastHost, { owner })
}

function mountInline(props: Record<string, unknown>): MountedApp {
  return mountComponent(InlineNotification, props)
}

function unmountComponent(mounted: MountedApp): void {
  mounted.app.unmount()
  mounted.host.remove()
  const index = mountedApps.indexOf(mounted)
  if (index >= 0) mountedApps.splice(index, 1)
}

function toastRoot(owner: ToastHostRole = 'primary'): HTMLElement | null {
  return document.querySelector<HTMLElement>(`[data-faros-toast-host="${owner}"]`)
}

function visibleToast(owner: ToastHostRole = 'primary'): HTMLElement | null {
  return toastRoot(owner)?.querySelector<HTMLElement>('.k-toast') ?? null
}

function enqueue(scope: string, options: Omit<ToastInput, 'scope'>): ToastId {
  return toast({ ...options, scope })
}

function setDocumentHidden(hidden: boolean): void {
  Object.defineProperty(document, 'hidden', { configurable: true, value: hidden })
  document.dispatchEvent(new Event('visibilitychange'))
}

afterEach(async () => {
  for (const scope of testScopes.splice(0)) clearToasts(scope)
  for (const mounted of mountedApps.splice(0)) {
    mounted.app.unmount()
    mounted.host.remove()
  }
  setDocumentHidden(false)
  await flush()
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('mounted Vue ToastHost behavior', () => {
  it('uses Teleport ownership so the primary host wins and fallback takes over', async () => {
    const scope = newScope('ownership')
    mountToast('fallback')
    const primary = mountToast('primary')
    await flush()

    const id = enqueue(scope, { message: 'shell notification', duration: 'persistent' })
    await flush()

    expect(document.querySelectorAll('[data-faros-toast-host]')).toHaveLength(2)
    expect(toastRoot('primary')?.dataset.active).toBe('true')
    expect(toastRoot('fallback')?.dataset.active).toBe('false')
    expect(toastRoot('primary')?.querySelectorAll('.k-toast')).toHaveLength(1)
    expect(toastRoot('primary')?.textContent).toContain('shell notification')
    expect(toastRoot('fallback')?.querySelectorAll('.k-toast')).toHaveLength(0)
    expect(toastRoot('fallback')?.getAttribute('aria-hidden')).toBe('true')

    unmountComponent(primary)
    await flush()

    expect(toastRoot('fallback')?.dataset.active).toBe('true')
    expect(toastRoot('fallback')?.querySelector<HTMLElement>(`[data-toast-id="${id}"]`)).not.toBeNull()
  })

  it('renders one toast, preempts only at a higher priority, and restores equal-priority FIFO order', async () => {
    const scope = newScope('priority')
    mountToast()
    await flush()

    const firstInfo = enqueue(scope, { kind: 'info', message: 'info one', duration: 'persistent' })
    const secondInfo = enqueue(scope, { kind: 'info', message: 'info two', duration: 'persistent' })
    const thirdInfo = enqueue(scope, { kind: 'info', message: 'info three', duration: 'persistent' })
    await flush()

    expect(toastRoot()?.querySelectorAll('.k-toast')).toHaveLength(1)
    expect(visibleToast()?.textContent).toContain('info one')

    const firstWarning = enqueue(scope, { kind: 'warning', message: 'warning one' })
    await flush()
    expect(visibleToast()?.textContent).toContain('warning one')

    const secondWarning = enqueue(scope, { kind: 'warning', message: 'warning two' })
    await flush()
    expect(visibleToast()?.textContent).toContain('warning one')
    expect(visibleToast()?.textContent).not.toContain('warning two')

    const error = enqueue(scope, { kind: 'error', message: 'error one' })
    await flush()
    expect(visibleToast()?.textContent).toContain('error one')

    dismissToast(error, scope)
    await flush()
    expect(visibleToast()?.textContent).toContain('warning one')

    dismissToast(firstWarning, scope)
    await flush()
    expect(visibleToast()?.textContent).toContain('warning two')

    dismissToast(secondWarning, scope)
    await flush()
    expect(visibleToast()?.textContent).toContain('info one')

    dismissToast(firstInfo, scope)
    await flush()
    expect(visibleToast()?.textContent).toContain('info two')

    dismissToast(secondInfo, scope)
    await flush()
    expect(visibleToast()?.textContent).toContain('info three')

    dismissToast(thirdInfo, scope)
    await settleToastLeave()
    expect(visibleToast()).toBeNull()
  })

  it('returns focus to the next toast action or dismiss control', async () => {
    const scope = newScope('focus-next-control')
    mountToast()
    await flush()

    enqueue(scope, { kind: 'info', message: 'first', duration: 'persistent' })
    const second = enqueue(scope, {
      kind: 'info',
      message: 'second',
      duration: 'persistent',
      action: { label: 'Review', run: () => {} },
    })
    await flush()

    const firstDismiss = toastRoot()?.querySelector<HTMLButtonElement>('.k-toast__dismiss')
    expect(firstDismiss).not.toBeNull()
    firstDismiss!.focus()
    await flush()
    firstDismiss!.click()
    await flush()

    expect(visibleToast()?.dataset.toastId).toBe(String(second))
    expect(document.activeElement).toBe(toastRoot()?.querySelector('.k-toast__action'))
  })

  it('pauses and resumes the remaining finite duration across hover, focus, and hidden-document state', async () => {
    vi.useFakeTimers()
    const scope = newScope('timers')
    mountToast()
    await flush()

    let dismissalReason: string | undefined
    enqueue(scope, {
      kind: 'info',
      message: 'finite notice',
      duration: 6000,
      onDismiss: reason => { dismissalReason = reason },
    })
    await flush()
    const card = visibleToast()
    expect(card).not.toBeNull()

    vi.advanceTimersByTime(2000)
    await flush()
    card!.dispatchEvent(new MouseEvent('mouseenter', { bubbles: true }))
    await flush()

    const outside = document.createElement('button')
    outside.type = 'button'
    document.body.appendChild(outside)
    card!.focus()
    await flush()
    setDocumentHidden(true)
    vi.advanceTimersByTime(10000)
    await flush()
    expect(visibleToast()?.textContent).toContain('finite notice')

    setDocumentHidden(false)
    await flush()
    vi.advanceTimersByTime(1000)
    await flush()
    expect(visibleToast()?.textContent).toContain('finite notice')

    card!.dispatchEvent(new MouseEvent('mouseleave', { bubbles: true }))
    await flush()
    expect(visibleToast()?.textContent).toContain('finite notice')

    outside.focus()
    await flush()
    expect(document.activeElement).toBe(outside)
    await vi.advanceTimersByTimeAsync(3999)
    await flush()
    expect(visibleToast()?.textContent).toContain('finite notice')
    await vi.advanceTimersByTimeAsync(1)
    await flush()
    expect(dismissalReason).toBe('timeout')
    await vi.advanceTimersByTimeAsync(200)
    await flush()
    expect(visibleToast()).toBeNull()
    outside.remove()
  })

  it('keeps warning, error, and action notices visible beyond their normal timer window', async () => {
    vi.useFakeTimers()
    const scope = newScope('persistence')
    mountToast()
    await flush()

    const warning = enqueue(scope, { kind: 'warning', message: 'warning persists' })
    await flush()
    vi.advanceTimersByTime(60000)
    await flush()
    expect(visibleToast()?.textContent).toContain('warning persists')

    dismissToast(warning, scope)
    await flush()
    const error = enqueue(scope, { kind: 'error', message: 'error persists' })
    await flush()
    vi.advanceTimersByTime(60000)
    await flush()
    expect(visibleToast()?.textContent).toContain('error persists')

    dismissToast(error, scope)
    await flush()
    const action = enqueue(scope, {
      kind: 'info',
      message: 'action persists',
      action: { label: 'Retry', run: () => {} },
    })
    await flush()
    vi.advanceTimersByTime(60000)
    await flush()
    expect(visibleToast()?.textContent).toContain('action persists')
    dismissToast(action, scope)
  })

  it('honors finite warning and error durations with the five-second floor', async () => {
    vi.useFakeTimers()
    const scope = newScope('numeric-duration')
    mountToast()
    await flush()

    let warningReason: string | undefined
    const warning = enqueue(scope, {
      kind: 'warning',
      message: 'timed warning',
      duration: 7000,
      onDismiss: reason => { warningReason = reason },
    })
    await flush()
    vi.advanceTimersByTime(6999)
    await flush()
    expect(visibleToast()?.textContent).toContain('timed warning')
    vi.advanceTimersByTime(1)
    await flush()
    expect(warningReason).toBe('timeout')
    vi.advanceTimersByTime(200)
    await flush()
    expect(visibleToast()).toBeNull()

    let errorReason: string | undefined
    const error = enqueue(scope, {
      kind: 'error',
      message: 'clamped error',
      duration: 1,
      onDismiss: reason => { errorReason = reason },
    })
    await flush()
    vi.advanceTimersByTime(4999)
    await flush()
    expect(visibleToast()?.textContent).toContain('clamped error')
    vi.advanceTimersByTime(1)
    await flush()
    expect(errorReason).toBe('timeout')
    vi.advanceTimersByTime(200)
    await flush()
    expect(visibleToast()).toBeNull()
    expect(warning).not.toBe(error)
  })

  it('settles an async action by ID after queue updates', async () => {
    const scope = newScope('action-queue-update')
    mountToast()
    await flush()

    let resolve!: () => void
    const pending = new Promise<void>(resolvePromise => { resolve = resolvePromise })
    const run = vi.fn(() => pending)
    const actionID = enqueue(scope, {
      kind: 'info',
      message: 'retry after refresh',
      action: { label: 'Retry', run },
    })
    await flush()

    const action = toastRoot()?.querySelector<HTMLButtonElement>('.k-toast__action')
    expect(action).not.toBeNull()
    action!.click()
    await flush()
    expect(action!.disabled).toBe(true)

    const interruption = enqueue(scope, { kind: 'error', message: 'temporary interruption' })
    await flush()
    expect(visibleToast()?.dataset.toastId).toBe(String(interruption))
    dismissToast(interruption, scope)
    await flush()
    expect(visibleToast()?.dataset.toastId).toBe(String(actionID))
    expect(visibleToast()?.querySelector<HTMLButtonElement>('.k-toast__action')?.disabled).toBe(true)

    resolve()
    await flush()
    await settleToastLeave()
    expect(run).toHaveBeenCalledTimes(1)
    expect(visibleToast()).toBeNull()
  })

  it('preserves timers and focus through primary-host takeover', async () => {
    vi.useFakeTimers()
    const scope = newScope('host-takeover-timer')
    const fallback = mountToast('fallback')
    const primary = mountToast('primary')
    await flush()

    enqueue(scope, { kind: 'info', message: 'handoff timer', duration: 6000 })
    await flush()
    vi.advanceTimersByTime(2000)
    await flush()
    const primaryDismiss = toastRoot('primary')?.querySelector<HTMLButtonElement>('.k-toast__dismiss')
    expect(primaryDismiss).not.toBeNull()
    primaryDismiss!.focus()
    await flush()

    unmountComponent(primary)
    await flush()
    const fallbackDismiss = toastRoot('fallback')?.querySelector<HTMLButtonElement>('.k-toast__dismiss')
    expect(toastRoot('fallback')?.dataset.active).toBe('true')
    expect(document.activeElement).toBe(fallbackDismiss)

    const outside = document.createElement('button')
    outside.type = 'button'
    document.body.appendChild(outside)
    outside.focus()
    await flush()
    vi.advanceTimersByTime(3999)
    await flush()
    expect(visibleToast('fallback')?.textContent).toContain('handoff timer')
    vi.advanceTimersByTime(1)
    await flush()
    vi.advanceTimersByTime(200)
    await flush()
    expect(visibleToast('fallback')).toBeNull()
    outside.remove()
    expect(fallback).toBeDefined()
  })

  it('preserves busy/error action state through takeover without re-running the action', async () => {
    const scope = newScope('host-takeover-action')
    const fallback = mountToast('fallback')
    const primary = mountToast('primary')
    await flush()

    let reject!: (reason?: unknown) => void
    const pending = new Promise<void>((_resolve, rejectPromise) => { reject = rejectPromise })
    const run = vi.fn(() => pending)
    enqueue(scope, {
      kind: 'info',
      message: 'handoff action',
      action: { label: 'Retry', run },
    })
    await flush()
    toastRoot('primary')?.querySelector<HTMLButtonElement>('.k-toast__action')?.click()
    await flush()
    expect(toastRoot('primary')?.querySelector<HTMLButtonElement>('.k-toast__action')?.disabled).toBe(true)

    unmountComponent(primary)
    await flush()
    const fallbackAction = toastRoot('fallback')?.querySelector<HTMLButtonElement>('.k-toast__action')
    expect(fallbackAction?.disabled).toBe(true)
    fallbackAction?.click()
    expect(run).toHaveBeenCalledTimes(1)

    reject(new Error('secret action details'))
    await flush()
    expect(visibleToast('fallback')?.textContent).toContain('Action failed. Try again.')
    expect(visibleToast('fallback')?.textContent).not.toContain('secret action details')
    expect(fallbackAction?.disabled).toBe(false)
    expect(fallbackAction?.getAttribute('aria-busy')).toBeNull()
    expect(fallback).toBeDefined()
  })

  it('closes after a successful action and guards a same-tick double click', async () => {
    const scope = newScope('action-success')
    mountToast()
    await flush()
    const run = vi.fn()
    const dismissed: string[] = []
    enqueue(scope, {
      kind: 'info',
      message: 'retry me',
      action: { label: 'Retry', run },
      onDismiss: reason => dismissed.push(reason),
    })
    await flush()

    const action = toastRoot()?.querySelector<HTMLButtonElement>('.k-toast__action')
    expect(action).not.toBeNull()
    action!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    action!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(run).toHaveBeenCalledTimes(1)
    await flush()
    await settleToastLeave()

    expect(visibleToast()).toBeNull()
    expect(dismissed).toEqual(['action'])
  })

  it('keeps a rejected action visible, persists it, and does not leak an unhandled rejection', async () => {
    const scope = newScope('action-rejection')
    mountToast()
    await flush()
    let reject!: (reason?: unknown) => void
    const pending = new Promise<void>((_resolve, rejectPromise) => { reject = rejectPromise })
    const run = vi.fn(() => pending)
    const unhandled: PromiseRejectionEvent[] = []
    const onUnhandled = (event: PromiseRejectionEvent) => {
      unhandled.push(event)
      event.preventDefault()
    }
    window.addEventListener('unhandledrejection', onUnhandled)
    enqueue(scope, {
      kind: 'info',
      message: 'retry failed',
      action: { label: 'Retry', run },
    })
    await flush()

    const action = toastRoot()?.querySelector<HTMLButtonElement>('.k-toast__action')
    expect(action).not.toBeNull()
    action!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    action!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(run).toHaveBeenCalledTimes(1)
    reject(new Error('secret backend details'))
    await flush()
    window.removeEventListener('unhandledrejection', onUnhandled)

    expect(visibleToast()?.textContent).toContain('Action failed. Try again.')
    expect(visibleToast()?.textContent).not.toContain('secret backend details')
    expect(action!.disabled).toBe(false)
    expect(action!.getAttribute('aria-busy')).toBeNull()
    vi.useFakeTimers()
    vi.advanceTimersByTime(60000)
    await flush()
    expect(visibleToast()?.textContent).toContain('retry failed')
    expect(unhandled).toHaveLength(0)
  })

  it('leaves a successful action visible when closeOnSuccess is false', async () => {
    const scope = newScope('action-keep')
    mountToast()
    await flush()
    const run = vi.fn()
    const id = enqueue(scope, {
      kind: 'info',
      message: 'saved but reviewable',
      action: { label: 'Review', run, closeOnSuccess: false },
    })
    await flush()

    const action = toastRoot()?.querySelector<HTMLButtonElement>('.k-toast__action')
    action!.click()
    await flush()

    expect(run).toHaveBeenCalledTimes(1)
    expect(visibleToast()?.textContent).toContain('saved but reviewable')
    expect(action!.disabled).toBe(false)
    dismissToast(id, scope)
  })

  it('does not steal focus when a toast arrives and only Escape from inside dismisses it', async () => {
    const scope = newScope('focus')
    mountToast()
    await flush()
    const origin = document.createElement('input')
    document.body.appendChild(origin)
    origin.focus()
    enqueue(scope, { message: 'focus-safe', duration: 'persistent' })
    await flush()

    expect(document.activeElement).toBe(origin)
    const outsideEscape = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    document.dispatchEvent(outsideEscape)
    expect(outsideEscape.defaultPrevented).toBe(false)
    expect(visibleToast()?.textContent).toContain('focus-safe')

    const card = visibleToast()!
    card.focus()
    await flush()
    const insideEscape = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    document.dispatchEvent(insideEscape)
    await flush()
    await settleToastLeave()

    expect(insideEscape.defaultPrevented).toBe(true)
    expect(visibleToast()).toBeNull()
    expect(document.activeElement).toBe(origin)
    origin.remove()
  })

  it('keeps status and alert channels mounted while routing auto, explicit, and off announcements', async () => {
    const scope = newScope('live-regions')
    mountToast()
    await flush()
    const root = toastRoot()!
    const status = root.querySelector<HTMLElement>('[role="status"]')!
    const alert = root.querySelector<HTMLElement>('[role="alert"]')!
    expect(status).not.toBeNull()
    expect(alert).not.toBeNull()

    const info = enqueue(scope, { kind: 'info', message: 'polite info', duration: 'persistent' })
    await flush()
    expect(status.textContent).toContain('polite info')
    expect(alert.textContent).toBe('')

    dismissToast(info, scope)
    await flush()
    const error = enqueue(scope, { kind: 'error', message: 'assertive error' })
    await flush()
    expect(status.textContent).toBe('')
    expect(alert.textContent).toContain('assertive error')

    dismissToast(error, scope)
    await flush()
    const politeError = enqueue(scope, { kind: 'error', message: 'explicit polite', announcement: 'polite' })
    await flush()
    expect(status.textContent).toContain('explicit polite')
    expect(alert.textContent).toBe('')

    dismissToast(politeError, scope)
    await flush()
    const silent = enqueue(scope, { kind: 'warning', message: 'silent warning', announcement: 'off' })
    await flush()
    expect(status.textContent).toBe('')
    expect(alert.textContent).toBe('')
    expect(visibleToast()?.textContent).toContain('silent warning')
    dismissToast(silent, scope)
  })
})

describe('mounted Vue InlineNotification behavior', () => {
  it('uses auto and off live-region roles and emits action and dismiss events', async () => {
    const action = vi.fn()
    const dismiss = vi.fn()
    const info = mountInline({
      tone: 'info',
      title: 'Connection',
      message: 'Reconnect required',
      actionLabel: 'Reconnect',
      dismissible: true,
      onAction: action,
      onDismiss: dismiss,
    })
    await flush()

    const infoNotification = info.host.querySelector<HTMLElement>('.k-inline-notification')!
    expect(infoNotification.getAttribute('role')).toBe('status')
    expect(infoNotification.getAttribute('aria-live')).toBe('polite')
    info.host.querySelector<HTMLButtonElement>('.k-inline-notification__action')!.click()
    info.host.querySelector<HTMLButtonElement>('.k-inline-notification__dismiss')!.click()
    expect(action).toHaveBeenCalledTimes(1)
    expect(dismiss).toHaveBeenCalledTimes(1)
    unmountComponent(info)

    const error = mountInline({ tone: 'error', message: 'Failed' })
    await flush()
    const errorNotification = error.host.querySelector<HTMLElement>('.k-inline-notification')!
    expect(errorNotification.getAttribute('role')).toBe('alert')
    expect(errorNotification.getAttribute('aria-live')).toBe('assertive')
    unmountComponent(error)

    const silent = mountInline({ tone: 'warning', message: 'No announcement', announce: 'off' })
    await flush()
    const silentNotification = silent.host.querySelector<HTMLElement>('.k-inline-notification')!
    expect(silentNotification.getAttribute('role')).toBeNull()
    expect(silentNotification.getAttribute('aria-live')).toBeNull()
  })
})
