// CANONICAL SOURCE — provider-sdk/portalkit. Do not edit vendored copies under
// providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`.
//
// Framework-free toast bus + renderer ("Violet Circuit" toast recipe,
// docs/design/components/toast.md). Tone is carried by the leading icon in the
// semantic colour; the error variant also turns the card border danger. No
// tinted backgrounds, no glow. One fixed bottom-right stack per document.
//
// The agents portal consumes this renderer through its compatibility adapter
// at providers/agents/portal/src/ui/toast.ts. Vue providers use the separate
// ToastHost transport and queue; the two share visual recipes, not lifecycle.
//
// Usage:
//   import { toast } from './portalkit/toast'
//   toast('ok', 'Instance provisioned')
//   toast('error', 'Build failed', { label: 'View log', run: () => open() })

import { ensureRailgridUIStyles } from './styles'

export type ToastKind = 'ok' | 'error' | 'info'

export interface ToastAction {
  label: string
  run: () => void | Promise<void>
}

const DURATION: Record<ToastKind, number> = { ok: 5000, info: 6000, error: 0 }
const MAX_VISIBLE = 3

const HOST_ID = 'k-toasts'

const ICONS: Record<ToastKind, string> = {
  ok: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.25" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>',
  error:
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.25" stroke-linecap="round" stroke-linejoin="round"><path d="M18 6 6 18M6 6l12 12"/></svg>',
  info: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.25" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/></svg>',
}

const X_ICON =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 6 6 18M6 6l12 12"/></svg>'

function host(): HTMLElement {
  ensureRailgridUIStyles()
  let el = document.getElementById(HOST_ID)
  if (!el) {
    el = document.createElement('div')
    el.id = HOST_ID
    el.setAttribute('role', 'status')
    el.setAttribute('aria-live', 'polite')
    document.body.appendChild(el)
  }
  return el
}

// Each standalone provider bundle gets its own module instance, but all of
// them render into the document-level toast host. Keep numeric IDs in a
// shared global sequence so one bundle cannot dismiss another bundle's card
// or timer after both start at sequence 1.
const TOAST_SEQUENCE_KEY = Symbol.for('railgrid.portalkit.toast.sequence')
type ToastGlobal = typeof globalThis & { [key: symbol]: unknown }
const toastGlobal = globalThis as ToastGlobal

function nextToastID(): number {
  const next = Number(toastGlobal[TOAST_SEQUENCE_KEY] ?? 0) + 1
  toastGlobal[TOAST_SEQUENCE_KEY] = next
  return next
}

const timers = new Map<number, ReturnType<typeof setTimeout>>()
const owned = new Set<number>()
const CLEANUP_KEY = Symbol.for('railgrid.portalkit.toast.cleanup')
const cleanup = (toastGlobal[CLEANUP_KEY] ||= new Map<number, () => void>()) as Map<number, () => void>
const ORIGINS_KEY = Symbol.for('railgrid.portalkit.toast.origins')
const origins = (toastGlobal[ORIGINS_KEY] ||= new Map<number, HTMLElement | null>()) as Map<number, HTMLElement | null>

function removeToast(id: number, restoreFocus: boolean): void {
  const card = document.getElementById(`k-toast-${id}`)
  const wasFocused = !!card?.contains(document.activeElement)
  const origin = origins.get(id)
  cleanup.get(id)?.()
  cleanup.delete(id)
  origins.delete(id)
  const t = timers.get(id)
  if (t) clearTimeout(t)
  timers.delete(id)
  owned.delete(id)
  card?.remove()
  const h = document.getElementById(HOST_ID)
  if (h && h.childElementCount === 0) h.remove()
  if (restoreFocus && wasFocused) {
    const next = h?.querySelector<HTMLElement>('.k-toast__action, .k-toast__dismiss')
    if (next?.isConnected) next.focus()
    else if (origin?.isConnected) origin.focus()
  }
}

export function dismissToast(id: number): void {
  removeToast(id, true)
}

export function clearToasts(): void {
  if (typeof document === 'undefined') return
  const current = document.activeElement instanceof HTMLElement ? document.activeElement : null
  const card = current?.closest<HTMLElement>('#k-toasts .k-toast')
  const focusedID = card ? Number(card.id.slice('k-toast-'.length)) : NaN
  const restore = Number.isInteger(focusedID) && owned.has(focusedID)
  const origin = restore ? origins.get(focusedID) : null
  for (const id of [...owned]) removeToast(id, false)
  if (restore) {
    const next = document.querySelector<HTMLElement>('#k-toasts .k-toast__action, #k-toasts .k-toast__dismiss')
    if (next?.isConnected) next.focus()
    else if (origin?.isConnected) origin.focus()
  }
}

export function toast(kind: ToastKind, message: string, action?: ToastAction): number {
  const h = host()
  // Follow new arrivals only while the reader is already at the end. Avoid
  // moving the stack underneath a pointer or a focused notification.
  const followNewest = h.scrollHeight - h.scrollTop - h.clientHeight <= 16
    && !h.contains(document.activeElement) && !h.matches(':hover')
  const id = nextToastID()
  owned.add(id)
  const active = document.activeElement instanceof HTMLElement ? document.activeElement : null
  const activeCard = active?.closest<HTMLElement>('#k-toasts .k-toast')
  const parentID = activeCard ? Number(activeCard.id.slice('k-toast-'.length)) : NaN
  origins.set(id, Number.isInteger(parentID) ? origins.get(parentID) ?? null : active)

  const card = document.createElement('div')
  card.id = `k-toast-${id}`
  card.className = `k-toast k-toast--${kind}`
  const persistent = kind === 'error' || Boolean(action)
  card.dataset.persistent = String(persistent)
  if (kind === 'error') card.setAttribute('role', 'alert')

  const ic = document.createElement('span')
  ic.className = 'k-toast__icon'
  ic.setAttribute('aria-hidden', 'true')
  ic.innerHTML = ICONS[kind]
  card.appendChild(ic)

  const msg = document.createElement('span')
  msg.className = 'k-toast__message'
  msg.textContent = message
  card.appendChild(msg)

  if (action) {
    const btn = document.createElement('button')
    btn.className = 'k-toast__action'
    btn.textContent = action.label
    let busy = false
    let failure: HTMLElement | null = null
    btn.addEventListener('click', async () => {
      if (busy) return
      busy = true
      failure?.remove()
      failure = null
      btn.textContent = `${action.label}…`
      btn.setAttribute('aria-busy', 'true')
      btn.setAttribute('aria-disabled', 'true')
      try {
        const result = action.run()
        if (result) await result
        // The user may have dismissed the toast while its action was pending.
        if (card.isConnected) dismissToast(id)
      } catch {
        if (!card.isConnected) return
        failure = document.createElement('span')
        failure.className = 'k-toast__action-error'
        failure.setAttribute('role', 'alert')
        failure.textContent = ' Action failed. Try again.'
        msg.appendChild(failure)
      } finally {
        busy = false
        btn.textContent = action.label
        btn.removeAttribute('aria-busy')
        btn.removeAttribute('aria-disabled')
      }
    })
    card.appendChild(btn)
  }

  const x = document.createElement('button')
  x.className = 'k-toast__dismiss'
  x.setAttribute('aria-label', 'Dismiss notification')
  x.innerHTML = X_ICON
  x.firstElementChild?.setAttribute('aria-hidden', 'true')
  x.addEventListener('click', () => dismissToast(id))
  card.appendChild(x)
  card.addEventListener('keydown', event => {
    if (event.key !== 'Escape' || event.defaultPrevented) return
    event.preventDefault()
    event.stopPropagation()
    dismissToast(id)
  })

  let remaining = DURATION[kind]
  let expiresAt = 0
  let hovered = false
  let focused = false
  const pause = () => {
    const timer = timers.get(id)
    if (timer === undefined) return
    remaining = Math.max(1, expiresAt - Date.now())
    clearTimeout(timer)
    timers.delete(id)
  }
  const updateTimer = () => {
    if (persistent) return
    if (hovered || focused || document.hidden) { pause(); return }
    if (timers.has(id)) return
    expiresAt = Date.now() + remaining
    timers.set(id, setTimeout(() => dismissToast(id), remaining))
  }
  const visibilityChanged = () => updateTimer()
  card.addEventListener('mouseenter', () => { hovered = true; updateTimer() })
  card.addEventListener('mouseleave', () => { hovered = false; updateTimer() })
  card.addEventListener('focusin', () => { focused = true; updateTimer() })
  card.addEventListener('focusout', event => {
    focused = event.relatedTarget instanceof Node && card.contains(event.relatedTarget)
    updateTimer()
  })
  if (!persistent) document.addEventListener('visibilitychange', visibilityChanged)
  cleanup.set(id, () => {
    pause()
    document.removeEventListener('visibilitychange', visibilityChanged)
    owned.delete(id)
  })

  h.appendChild(card)
  while (h.childElementCount > MAX_VISIBLE) {
    const transient = Array.from(h.querySelectorAll<HTMLElement>('.k-toast[data-persistent="false"]'))
      .find(item => !item.contains(document.activeElement))
    if (!transient) break
    const transientID = Number(transient.id.slice('k-toast-'.length))
    if (Number.isInteger(transientID)) dismissToast(transientID)
    else transient.remove()
  }
  if (card.isConnected) {
    if (followNewest) h.scrollTop = h.scrollHeight
    updateTimer()
  }
  return id
}
