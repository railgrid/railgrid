// Toasts replace the old single note bar (one string reused for success and
// failure, rendered as a clickable div). A module-level bus lets any mutation
// raise one without threading a callback through the component tree.

import { html, type TemplateResult } from 'lit'
import { repeat } from 'lit/directives/repeat.js'
import { LightElement } from './base'
import { icon } from './icon'

export type ToastKind = 'ok' | 'error' | 'info'

export interface Toast {
  id: number
  kind: ToastKind
  message: string
  // action renders a single follow-up button (e.g. "View run").
  action?: { label: string; run: () => void }
}

type Listener = (toasts: Toast[]) => void

const DURATION: Record<ToastKind, number> = { ok: 4000, info: 6000, error: 9000 }

let seq = 0
let toasts: Toast[] = []
const listeners = new Set<Listener>()

function emit(): void {
  for (const l of listeners) l(toasts)
}

export function subscribeToasts(l: Listener): () => void {
  listeners.add(l)
  l(toasts)
  return () => listeners.delete(l)
}

export function toast(kind: ToastKind, message: string, action?: Toast['action']): number {
  const t: Toast = { id: ++seq, kind, message, action }
  toasts = [...toasts, t]
  emit()
  setTimeout(() => dismissToast(t.id), DURATION[kind])
  return t.id
}

export function dismissToast(id: number): void {
  const next = toasts.filter((t) => t.id !== id)
  if (next.length === toasts.length) return
  toasts = next
  emit()
}

export function clearToasts(): void {
  toasts = []
  emit()
}

// <agents-toasts> is rendered once by the shell. aria-live=polite so a
// screen reader announces results of actions the user just took.
export class ToastHost extends LightElement {
  private items: Toast[] = []
  private unsubscribe: (() => void) | null = null

  connectedCallback(): void {
    super.connectedCallback()
    this.unsubscribe = subscribeToasts((t) => {
      this.items = t
      this.requestUpdate()
    })
  }

  disconnectedCallback(): void {
    this.unsubscribe?.()
    this.unsubscribe = null
    super.disconnectedCallback()
  }

  render(): TemplateResult {
    return html`<div class="agents-toasts" role="status" aria-live="polite">
      ${repeat(
        this.items,
        (t) => t.id,
        (t) => html`
          <div class="agents-toast agents-toast-${t.kind}">
            <span class="agents-toast-ic">${icon(t.kind === 'error' ? 'x' : t.kind === 'ok' ? 'check' : 'circle')}</span>
            <span class="agents-toast-msg">${t.message}</span>
            ${t.action
              ? html`<button
                  class="agents-toast-action"
                  @click=${() => {
                    t.action?.run()
                    dismissToast(t.id)
                  }}
                >
                  ${t.action.label}
                </button>`
              : null}
            <button class="agents-toast-x" aria-label="Dismiss notification" @click=${() => dismissToast(t.id)}>
              ${icon('x')}
            </button>
          </div>
        `,
      )}
    </div>`
  }
}

if (!customElements.get('agents-toasts')) customElements.define('agents-toasts', ToastHost)
