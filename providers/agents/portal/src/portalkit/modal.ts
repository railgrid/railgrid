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
import { ensureFarosUIStyles } from './styles'

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

function esc(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c] as string)
}

// dialog is the shared renderer: builds the overlay, wires confirm/cancel/escape/
// backdrop, and resolves once. `showCancel=false` gives an alert (single button).
function dialog(opts: ConfirmOptions, showCancel: boolean): Promise<boolean> {
  ensureFarosUIStyles()
  return new Promise<boolean>((resolve) => {
    const overlay = document.createElement('div')
    overlay.className = 'k-modal-overlay'
    const danger = opts.danger ? ' k-modal--danger' : ''
    overlay.innerHTML = `
      <div class="k-modal k-modal--vanilla${danger}" role="dialog" aria-modal="true" aria-label="${esc(opts.title)}">
        <div class="k-modal__head">
          <span class="k-modal__icon">${ic(opts.danger ? 'trash' : 'circle')}</span>
          <h2 class="k-modal__title">${esc(opts.title)}</h2>
        </div>
        ${opts.message ? `<div class="k-modal__body">${esc(opts.message)}</div>` : ''}
        <div class="k-modal__foot">
          ${showCancel ? `<button class="k-modal-btn k-modal-btn--cancel" data-k-modal-cancel>${esc(opts.cancelLabel || 'Cancel')}</button>` : ''}
          <button class="k-modal-btn k-modal-btn--confirm${opts.danger ? ' k-modal-btn--danger' : ''}" data-k-modal-confirm>${esc(opts.confirmLabel || (showCancel ? 'Confirm' : 'OK'))}</button>
        </div>
      </div>`
    let done = false
    const previouslyFocused = document.activeElement as HTMLElement | null
    const close = (v: boolean): void => {
      if (done) return
      done = true
      overlay.removeEventListener('keydown', onKey)
      overlay.remove()
      previouslyFocused?.focus?.()
      resolve(v)
    }
    // Keydown is scoped to the dialog (not window) and Enter is deliberately
    // NOT handled: the focused button activates on Enter natively, so focus
    // decides the outcome. A global Enter-confirms handler used to fire a
    // destructive confirm no matter where focus was.
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        close(false)
        return
      }
      if (e.key !== 'Tab') return
      // Focus trap: keep Tab inside the dialog so the underlying page can't be
      // reached while a modal is open.
      const focusables = [...overlay.querySelectorAll<HTMLElement>('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])')].filter(
        (el) => !el.hasAttribute('disabled'),
      )
      if (!focusables.length) return
      const first = focusables[0]
      const last = focusables[focusables.length - 1]
      const active = document.activeElement as HTMLElement | null
      if (e.shiftKey && (active === first || !overlay.contains(active))) {
        e.preventDefault()
        last.focus()
      } else if (!e.shiftKey && active === last) {
        e.preventDefault()
        first.focus()
      }
    }
    overlay.addEventListener('click', (e) => {
      if (e.target === overlay) close(false) // backdrop
    })
    overlay.querySelector('[data-k-modal-cancel]')?.addEventListener('click', () => close(false))
    overlay.querySelector('[data-k-modal-confirm]')?.addEventListener('click', () => close(true))
    overlay.addEventListener('keydown', onKey)
    document.body.appendChild(overlay)
    // Danger dialogs focus Cancel so a reflexive Enter/Space cancels rather
    // than deletes; non-destructive ones focus the primary action.
    const initial = opts.danger && showCancel ? '[data-k-modal-cancel]' : '[data-k-modal-confirm]'
    overlay.querySelector<HTMLElement>(initial)?.focus()
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
