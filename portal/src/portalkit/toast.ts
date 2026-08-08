// CANONICAL SOURCE — provider-sdk/portalkit. Do not edit vendored copies under
// providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`.
//
// Framework-free toast bus + renderer ("Violet Circuit" toast recipe,
// docs/design-book.md §10). Tone is carried by the leading icon in the
// semantic colour; the error variant also turns the card border danger. No
// tinted backgrounds, no glow. One fixed bottom-right stack per document.
//
// The agents portal predates this file and renders the same recipe through
// its own lit host (providers/agents/portal/src/ui/toast.ts) — visuals must
// stay identical between the two.
//
// Usage:
//   import { toast } from './portalkit/toast'
//   toast('ok', 'Instance provisioned')
//   toast('error', 'Build failed', { label: 'View log', run: () => open() })

export type ToastKind = 'ok' | 'error' | 'info'

export interface ToastAction {
  label: string
  run: () => void
}

const DURATION: Record<ToastKind, number> = { ok: 4000, info: 6000, error: 9000 }
const MAX_VISIBLE = 3

const STYLE_ID = 'pk-toast-styles'
const HOST_ID = 'pk-toasts'

const ICONS: Record<ToastKind, string> = {
  ok: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.25" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>',
  error:
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.25" stroke-linecap="round" stroke-linejoin="round"><path d="M18 6 6 18M6 6l12 12"/></svg>',
  info: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.25" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/></svg>',
}

const X_ICON =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 6 6 18M6 6l12 12"/></svg>'

function ensureStyles(): void {
  if (document.getElementById(STYLE_ID)) return
  const s = document.createElement('style')
  s.id = STYLE_ID
  s.textContent = `
#${HOST_ID} { position: fixed; right: 16px; bottom: 16px; z-index: 2147483001;
  display: flex; flex-direction: column; align-items: flex-end; gap: 8px; max-width: 360px; }
.pk-toast { display: flex; align-items: flex-start; gap: 9px; padding: 10px 12px; border-radius: 6px;
  font-family: var(--font-sans, ui-sans-serif, system-ui, sans-serif); font-size: 13px; line-height: 1.45;
  background: var(--color-surface-raised, #111320); color: var(--color-text-primary, #e9e9f2);
  border: 1px solid var(--color-border-default, rgba(255,255,255,.11));
  box-shadow: 0 12px 34px rgba(0, 0, 0, 0.4);
  animation: pk-toast-in 0.16s cubic-bezier(0.2, 0.8, 0.3, 1); }
@keyframes pk-toast-in { from { opacity: 0; transform: translateY(8px); } }
.pk-toast-ic { flex: none; margin-top: 1px; width: 14px; height: 14px; }
.pk-toast-ic svg { display: block; width: 100%; height: 100%; }
.pk-toast-ok .pk-toast-ic { color: var(--color-success, #2fd6a0); }
.pk-toast-error .pk-toast-ic { color: var(--color-danger, #ff5d5d); }
.pk-toast-info .pk-toast-ic { color: var(--color-accent, #8b6bff); }
.pk-toast-error { border-color: var(--color-danger, #ff5d5d); }
.pk-toast-msg { flex: 1; overflow-wrap: anywhere; }
.pk-toast-action { flex: none; border: 1px solid color-mix(in srgb, var(--color-accent, #8b6bff) 35%, transparent);
  background: var(--color-accent-subtle, rgba(139,107,255,.14)); color: var(--color-accent, #8b6bff);
  border-radius: 4px; padding: 2px 8px; font: inherit; font-size: 12px; font-weight: 600; cursor: pointer; }
.pk-toast-action:hover { background: color-mix(in srgb, var(--color-accent, #8b6bff) 22%, transparent); }
.pk-toast-x { flex: none; display: grid; place-items: center; width: 20px; height: 20px; margin: -2px -4px 0 0;
  border: 0; background: transparent; color: var(--color-text-muted, #5d5f78); border-radius: 4px; cursor: pointer; }
.pk-toast-x svg { width: 12px; height: 12px; }
.pk-toast-x:hover { background: var(--color-surface-hover, #1e2033); color: inherit; }
@media (prefers-reduced-motion: reduce) { .pk-toast { animation: none; } }
@media (max-width: 640px) { #${HOST_ID} { left: 12px; right: 12px; max-width: none; align-items: stretch; } }
`
  document.head.appendChild(s)
}

function host(): HTMLElement {
  ensureStyles()
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

let seq = 0
const timers = new Map<number, ReturnType<typeof setTimeout>>()

export function dismissToast(id: number): void {
  const t = timers.get(id)
  if (t) clearTimeout(t)
  timers.delete(id)
  document.getElementById(`pk-toast-${id}`)?.remove()
  const h = document.getElementById(HOST_ID)
  if (h && h.childElementCount === 0) h.remove()
}

export function clearToasts(): void {
  for (const id of [...timers.keys()]) dismissToast(id)
}

export function toast(kind: ToastKind, message: string, action?: ToastAction): number {
  const h = host()
  const id = ++seq

  const card = document.createElement('div')
  card.id = `pk-toast-${id}`
  card.className = `pk-toast pk-toast-${kind}`
  if (kind === 'error') card.setAttribute('role', 'alert')

  const ic = document.createElement('span')
  ic.className = 'pk-toast-ic'
  ic.innerHTML = ICONS[kind]
  card.appendChild(ic)

  const msg = document.createElement('span')
  msg.className = 'pk-toast-msg'
  msg.textContent = message
  card.appendChild(msg)

  if (action) {
    const btn = document.createElement('button')
    btn.className = 'pk-toast-action'
    btn.textContent = action.label
    btn.addEventListener('click', () => {
      action.run()
      dismissToast(id)
    })
    card.appendChild(btn)
  }

  const x = document.createElement('button')
  x.className = 'pk-toast-x'
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
