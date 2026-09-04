// CANONICAL SOURCE — provider-sdk/portalkit. Do not edit vendored copies under
// providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`.
//
// Framework-free toast bus + renderer ("Violet Circuit" toast recipe,
// docs/design-book.md §10). Tone is carried by the leading icon in the
// semantic colour; the error variant also turns the card border danger. No
// tinted backgrounds, no glow. One fixed bottom-right stack per document.
//
// The agents portal consumes this renderer through its compatibility adapter
// at providers/agents/portal/src/ui/toast.ts, so every provider shares this
// document-level visual and lifecycle implementation.
//
// Usage:
//   import { toast } from './portalkit/toast'
//   toast('ok', 'Instance provisioned')
//   toast('error', 'Build failed', { label: 'View log', run: () => open() })

import { ensureFarosUIStyles } from './styles'

export type ToastKind = 'ok' | 'error' | 'info'

export interface ToastAction {
  label: string
  run: () => void
}

const DURATION: Record<ToastKind, number> = { ok: 4000, info: 6000, error: 9000 }
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
  ensureFarosUIStyles()
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
const TOAST_SEQUENCE_KEY = Symbol.for('faros.portalkit.toast.sequence')
type ToastGlobal = typeof globalThis & { [key: symbol]: unknown }
const toastGlobal = globalThis as ToastGlobal

function nextToastID(): number {
  const next = Number(toastGlobal[TOAST_SEQUENCE_KEY] ?? 0) + 1
  toastGlobal[TOAST_SEQUENCE_KEY] = next
  return next
}

const timers = new Map<number, ReturnType<typeof setTimeout>>()

export function dismissToast(id: number): void {
  const t = timers.get(id)
  if (t) clearTimeout(t)
  timers.delete(id)
  document.getElementById(`k-toast-${id}`)?.remove()
  const h = document.getElementById(HOST_ID)
  if (h && h.childElementCount === 0) h.remove()
}

export function clearToasts(): void {
  for (const id of [...timers.keys()]) dismissToast(id)
}

export function toast(kind: ToastKind, message: string, action?: ToastAction): number {
  const h = host()
  const id = nextToastID()

  const card = document.createElement('div')
  card.id = `k-toast-${id}`
  card.className = `k-toast k-toast--${kind}`
  if (kind === 'error') card.setAttribute('role', 'alert')

  const ic = document.createElement('span')
  ic.className = 'k-toast__icon'
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
    btn.addEventListener('click', () => {
      action.run()
      dismissToast(id)
    })
    card.appendChild(btn)
  }

  const x = document.createElement('button')
  x.className = 'k-toast__dismiss'
  x.setAttribute('aria-label', 'Dismiss notification')
  x.innerHTML = X_ICON
  x.addEventListener('click', () => dismissToast(id))
  card.appendChild(x)

  // Pause auto-dismiss while hovered; resume with the full duration on leave.
  const arm = () => timers.set(id, setTimeout(() => dismissToast(id), DURATION[kind]))
  card.addEventListener('mouseenter', () => {
    const t = timers.get(id)
    if (t) clearTimeout(t)
  })
  card.addEventListener('mouseleave', arm)

  h.appendChild(card)
  while (h.childElementCount > MAX_VISIBLE) h.firstElementChild?.remove()
  arm()
  return id
}
