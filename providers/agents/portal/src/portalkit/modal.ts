// CANONICAL SOURCE — provider-sdk/portalkit. Do not edit vendored copies under
// providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`.
//
// Promise-based in-page confirm / alert modals for the string-building
// (vanilla-TS) provider portals — agents, kuery, quickstart — replacing the
// browser's native window.confirm / window.alert so destructive actions use an
// on-brand dialog that matches the portal's styling (light/dark via the host's
// CSS design tokens). Self-contained: injects its own <style> once and renders
// a single overlay appended to <body>.
//
// Usage:
//   if (await confirmModal({ title: 'Delete agent?', message: '…', danger: true })) { … }
//   await alertModal('Done', 'The thing happened.')

import { ic } from './icons'

export interface ConfirmOptions {
  title: string
  // Optional supporting text under the title. Plain text (rendered escaped);
  // newlines become line breaks.
  message?: string
  confirmLabel?: string
  cancelLabel?: string
  // danger styles the confirm button as a destructive action (delete/remove).
  danger?: boolean
}

const STYLE_ID = 'kedge-portalkit-modal-css'

function ensureStyles(): void {
  if (document.getElementById(STYLE_ID)) return
  const s = document.createElement('style')
  s.id = STYLE_ID
  s.textContent = `
.pk-modal-overlay { position: fixed; inset: 0; z-index: 2147483000; display: grid; place-items: center; padding: 24px;
  background: color-mix(in srgb, var(--color-text-primary, #0b1220) 40%, transparent); backdrop-filter: blur(2px);
  animation: pk-fade .14s ease; }
.pk-modal { width: min(440px, 100%); border-radius: 16px; overflow: hidden;
  background: var(--color-surface-raised, #fff); color: var(--color-text-primary, #10151f);
  border: 1px solid var(--color-border-default, rgba(0,0,0,.12)); box-shadow: 0 24px 70px rgba(10,20,40,.42);
  font: 14px/1.5 ui-sans-serif, system-ui, -apple-system, sans-serif; animation: pk-rise .18s cubic-bezier(.2,.8,.3,1); }
.pk-modal-head { display: flex; align-items: center; gap: 11px; padding: 18px 20px 4px; }
.pk-modal-ic { width: 34px; height: 34px; flex: none; display: grid; place-items: center; border-radius: 10px;
  background: var(--color-accent-subtle, rgba(109,79,224,.12)); color: var(--color-accent, #6d4fe0); }
.pk-modal.danger .pk-modal-ic { background: var(--color-danger-surface, rgba(239,68,68,.12)); color: var(--color-danger, #ef4444); }
.pk-modal-ic svg { width: 20px; height: 20px; }
.pk-modal-title { margin: 0; font-size: 16px; font-weight: 650; }
.pk-modal-body { padding: 6px 20px 4px 65px; color: var(--color-text-secondary, #4a5568); white-space: pre-wrap; overflow-wrap: anywhere; }
.pk-modal-foot { display: flex; justify-content: flex-end; gap: 10px; padding: 16px 20px 20px; }
.pk-modal-btn { padding: 8px 16px; border-radius: 9px; border: 0; cursor: pointer; font: inherit; font-weight: 600; font-size: 13.5px; }
.pk-modal-btn.cancel { background: var(--color-surface-overlay, rgba(0,0,0,.05)); color: var(--color-text-secondary, #4a5568);
  border: 1px solid var(--color-border-default, transparent); }
.pk-modal-btn.cancel:hover { background: var(--color-surface-hover, rgba(0,0,0,.08)); color: var(--color-text-primary, #10151f); }
.pk-modal-btn.confirm { background: var(--color-accent, #6d4fe0); color: #fff; }
.pk-modal-btn.confirm:hover { background: var(--color-accent-hover, #5b3fd0); }
.pk-modal.danger .pk-modal-btn.confirm { background: var(--color-danger, #ef4444); }
.pk-modal.danger .pk-modal-btn.confirm:hover { background: var(--color-danger-hover, #dc2626); }
@keyframes pk-fade { from { opacity: 0; } }
@keyframes pk-rise { from { opacity: 0; transform: translateY(10px) scale(.98); } }
@media (prefers-reduced-motion: reduce) { .pk-modal-overlay, .pk-modal { animation: none; } }
`
  document.head.appendChild(s)
}

function esc(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c] as string)
}

// dialog is the shared renderer: builds the overlay, wires confirm/cancel/escape/
// backdrop, and resolves once. `showCancel=false` gives an alert (single button).
function dialog(opts: ConfirmOptions, showCancel: boolean): Promise<boolean> {
  ensureStyles()
  return new Promise<boolean>((resolve) => {
    const overlay = document.createElement('div')
    overlay.className = 'pk-modal-overlay'
    const danger = opts.danger ? ' danger' : ''
    overlay.innerHTML = `
      <div class="pk-modal${danger}" role="dialog" aria-modal="true" aria-label="${esc(opts.title)}">
        <div class="pk-modal-head">
          <span class="pk-modal-ic">${ic(opts.danger ? 'trash' : 'circle')}</span>
          <h2 class="pk-modal-title">${esc(opts.title)}</h2>
        </div>
        ${opts.message ? `<div class="pk-modal-body">${esc(opts.message)}</div>` : ''}
        <div class="pk-modal-foot">
          ${showCancel ? `<button class="pk-modal-btn cancel" data-cancel>${esc(opts.cancelLabel || 'Cancel')}</button>` : ''}
          <button class="pk-modal-btn confirm" data-confirm>${esc(opts.confirmLabel || (showCancel ? 'Confirm' : 'OK'))}</button>
        </div>
      </div>`
    let done = false
    const close = (v: boolean): void => {
      if (done) return
      done = true
      window.removeEventListener('keydown', onKey)
      overlay.remove()
      resolve(v)
    }
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') close(false)
      else if (e.key === 'Enter') close(true)
    }
    overlay.addEventListener('click', (e) => {
      if (e.target === overlay) close(false) // backdrop
    })
    overlay.querySelector('[data-cancel]')?.addEventListener('click', () => close(false))
    overlay.querySelector('[data-confirm]')?.addEventListener('click', () => close(true))
    window.addEventListener('keydown', onKey)
    document.body.appendChild(overlay)
    overlay.querySelector<HTMLElement>('[data-confirm]')?.focus()
  })
}

// confirmModal resolves true on confirm, false on cancel / escape / backdrop.
export function confirmModal(opts: ConfirmOptions): Promise<boolean> {
  return dialog(opts, true)
}

// alertModal shows a single-button notice and resolves when dismissed.
export function alertModal(title: string, message?: string): Promise<void> {
  return dialog({ title, message }, false).then(() => undefined)
}
