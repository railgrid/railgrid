// Agents keeps this adapter for its existing mutation/view imports and tests,
// while the actual DOM renderer is the canonical framework-free PortalKit
// toast bus. Keeping the local subscription API preserves the provider test seam
// without maintaining a second visual implementation.

import { clearToasts as clearRenderedToasts, dismissToast as dismissRenderedToast, toast as renderToast } from '../portalkit/toast'

export type ToastKind = 'ok' | 'error' | 'info'

export interface Toast {
  id: number
  kind: ToastKind
  message: string
  action?: { label: string; run: () => void }
}

type Listener = (toasts: Toast[]) => void

const HOST_ID = 'k-toasts'
const TOAST_ID_PREFIX = 'k-toast-'

let toasts: Toast[] = []
const listeners = new Set<Listener>()
let lifecycleObserver: MutationObserver | null = null

function emit(): void {
  for (const listener of listeners) listener(toasts)
}

// PortalKit owns the DOM lifecycle (timers, action buttons, and the visible
// cap), while this adapter owns the subscription state used by Agents tests
// and legacy callers. Observe the renderer's host so those two views cannot
// drift when PortalKit removes a card without going through this module.
function ensureLifecycleObserver(): void {
  if (typeof document === 'undefined' || typeof MutationObserver === 'undefined') return
  if (lifecycleObserver) return

  lifecycleObserver = new MutationObserver(reconcileWithRenderer)
  lifecycleObserver.observe(document, { childList: true, subtree: true })
}

function renderedToastIDs(): Set<number> {
  const rendered = new Set<number>()
  const host = document.getElementById(HOST_ID)
  if (!host) return rendered

  for (const child of Array.from(host.children)) {
    if (!child.id.startsWith(TOAST_ID_PREFIX)) continue
    const id = Number(child.id.slice(TOAST_ID_PREFIX.length))
    if (Number.isInteger(id)) rendered.add(id)
  }
  return rendered
}

function reconcileWithRenderer(): boolean {
  if (typeof document === 'undefined' || !toasts.length) return false

  const rendered = renderedToastIDs()
  const next = toasts.filter((item) => rendered.has(item.id))
  if (next.length === toasts.length) return false

  // Dropping the whole Toast object is intentional: action callbacks must not
  // stay reachable after the corresponding DOM card was dismissed or evicted.
  toasts = next
  emit()
  return true
}

export function subscribeToasts(listener: Listener): () => void {
  ensureLifecycleObserver()
  reconcileWithRenderer()
  listeners.add(listener)
  listener(toasts)
  return () => listeners.delete(listener)
}

export function toast(kind: ToastKind, message: string, action?: Toast['action']): number {
  ensureLifecycleObserver()
  reconcileWithRenderer()
  const id = renderToast(kind, message, action)
  toasts = [...toasts, { id, kind, message, action }]
  // Max-visible eviction is synchronous in the renderer. Reconcile here as
  // well as from the observer so the adapter is correct before the next turn.
  const reconciled = reconcileWithRenderer()
  if (!reconciled) emit()
  return id
}

export function dismissToast(id: number): void {
  dismissRenderedToast(id)
  const next = toasts.filter((item) => item.id !== id)
  if (next.length === toasts.length) return
  toasts = next
  emit()
}

export function clearToasts(): void {
  clearRenderedToasts()
  if (!toasts.length) return
  toasts = []
  emit()
}
