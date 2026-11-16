// Compatibility API for Agents callers, backed by the shared document queue.
import { clearToasts as clearScoped, dismissToast as dismissScoped, subscribeToasts as subscribeShared, toast as enqueue } from '../portalkit/toast'

export type ToastKind = 'ok' | 'error' | 'info'
export interface Toast {
  id: number
  kind: ToastKind
  message: string
  action?: { label: string; run: () => void | Promise<void> }
}
type Listener = (toasts: Toast[]) => void

// A separate bundle instance must never clear another instance's notices.
const scopeKey = Symbol.for('railgrid.agents.toast.scope-sequence')
const shared = globalThis as typeof globalThis & { [key: symbol]: unknown }
const sequence = Number(shared[scopeKey] ?? 0) + 1
shared[scopeKey] = sequence
const scope = `agents:${sequence}`

export function subscribeToasts(listener: Listener): () => void {
  return subscribeShared(commands => listener(commands
    .filter(command => command.scope === scope)
    .sort((a, b) => a.id - b.id)
    .map(command => ({ id: command.id, kind: command.kind as ToastKind, message: command.message, action: command.action }))))
}

export function toast(kind: ToastKind, message: string, action?: Toast['action']): number {
  return enqueue(kind, message, { scope, action })
}

export function dismissToast(id: number): void { dismissScoped(id, scope) }
export function clearToasts(): void { clearScoped(scope) }
