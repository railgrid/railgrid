import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const toast = readFileSync(new URL('./toast.ts', import.meta.url), 'utf8')
const host = readFileSync(new URL('./ToastHost.vue', import.meta.url), 'utf8')
const inline = readFileSync(new URL('./InlineNotification.vue', import.meta.url), 'utf8')
const styles = readFileSync(new URL('../portalkit/faros-ui.css', import.meta.url), 'utf8')

test('Vue toast transport is versioned and shared across independent bundles', () => {
  assert.match(toast, /TOAST_TRANSPORT_VERSION = 1/)
  assert.match(toast, /TOAST_TRANSPORT_EVENT = ['"]faros:portalkit:toast['"]/
  )
  assert.match(toast, /Symbol\.for\('faros\.portalkit\.vue\.toast\.bridge\.v1'\)/)
  assert.match(toast, /version: TOAST_TRANSPORT_VERSION/)
  assert.match(toast, /doc\.dispatchEvent\(new CustomEvent<ToastTransportDetail>/)
})

test('Vue toast queue protects recovery notices and preempts by priority', () => {
  assert.match(toast, /const MIN_DURATION = 5000/)
  assert.match(toast, /const DEFAULT_DURATION: Record<ToastKind, number> = \{[\s\S]*ok: 5000,[\s\S]*info: 6000,/)
  assert.match(toast, /Math\.max\(MIN_DURATION, requested \?\? DEFAULT_DURATION\[kind\]\)/)
  assert.match(toast, /Boolean\(options\.action\)/)
  assert.match(toast, /const requested = typeof options\.duration/)
  assert.match(host, /const MAX_VISIBLE = 1/)
  assert.match(host, /function rebalance\(/)
  assert.match(host, /next\.priority > visible\.priority/)
  assert.match(host, /pauseEntry\(visible\)/)
})

test('Vue host state is transferable and async actions settle through the shared ID ledger', () => {
  assert.match(toast, /export interface ToastRuntimeState/)
  assert.match(toast, /export interface ToastHostState/)
  assert.match(toast, /export function updateToastRuntime\(/)
  assert.match(toast, /stateProvider\?: \(\) => ToastHostState \| null/)
  assert.match(toast, /currentHandoff\(bridge, registration\.stateProvider\?\.\(\)\)/)
  assert.match(host, /function captureHandoff\(/)
  assert.match(host, /updateToastRuntime\(id, \{ actionBusy: true, actionError: null \}\)/)
  assert.match(host, /dismissToast\(id, undefined, 'action'\)/)
  assert.match(host, /entries\.value\.some\(candidate => candidate\.id === id\)/)
})

test('Vue toast API supports legacy calls plus scoped dedupe and clear', () => {
  assert.match(toast, /export type ToastAnnouncement = 'auto' \| 'polite' \| 'assertive' \| 'off'/)
  assert.match(toast, /export type ToastDuration = number \| 'persistent'/)
  assert.match(toast, /export type ToastId = number/)
  assert.match(toast, /closeOnSuccess\?: boolean/)
  const toastOptions = toast.slice(
    toast.indexOf('export interface ToastOptions'),
    toast.indexOf('/** Full object input'),
  )
  assert.match(toastOptions, /action\?: ToastAction/)
  assert.doesNotMatch(toastOptions, /\bmessage\b|\bkind\b/)
  assert.match(toast, /export interface ToastInput extends ToastOptions/)
  assert.match(toast, /export function toast\(kind: ToastKind, message: string, actionOrOptions\?: ToastAction \| ToastOptions\)/)
  assert.match(toast, /export function toast\(options: ToastInput\)/)
  assert.match(toast, /command\.scope === scope && command\.dedupeKey === dedupeKey/)
  assert.doesNotMatch(toast, /command\.source === source && command\.scope === scope/)
  assert.match(toast, /const source = options\.source\?\.trim\(\) \|\| ''/)
  assert.match(toast, /removeCommand\(deduped, 'replaced', true\)/)
  assert.match(toast, /MAX_SEEN_OPERATIONS = 256/)
  assert.match(toast, /export function clearToasts\(scope: string\)/)
  assert.match(toast, /export function dismissToast\(\n  id: ToastId,/)
  assert.match(toast, /export function useToast\(defaults: ToastScope = \{\}\)/)
  assert.match(toast, /const scopedClear = \(scope\?: string\): void => \{[\s\S]*const targetScope = scope \?\? defaults\.scope[\s\S]*if \(targetScope === undefined\) return[\s\S]*clearToasts\(targetScope\)/)
})

test('only the active host renders, with pre-mounted status and alert channels', () => {
  assert.match(host, /owner\?: ToastHostRole/)
  assert.match(host, /owner: 'primary'/)
  assert.match(host, /data-faros-toast-host/)
  assert.match(host, /class="k-toast-host__channel k-toast-host__channel--status" role="status"/)
  assert.match(host, /class="k-toast-host__channel k-toast-host__channel--alert" role="alert"/)
  assert.match(host, /<Teleport to="body">/)
  assert.match(host, /event\.key !== 'Escape'/)
  assert.match(host, /root\.value\?\.contains\(document\.activeElement\)/)
  assert.match(host, /visibilityChanged\(/)
  assert.match(host, /entry\.hovered \|\| entry\.focused/)
  assert.match(host, /function entryForEvent\(event: Event\): ToastEntry \| null/)
  assert.doesNotMatch(host, /:key="activeToast\.id"/)
  assert.match(host, /closeOnSuccess/)
  assert.match(host, /focusOrigins\.set\(id, item\.focusOrigin\)/)
  assert.match(host, /function focusControl\(id: number\)/)
  assert.match(host, /\.k-toast__action:not\(:disabled\), \.k-toast__dismiss/)
  assert.doesNotMatch(host, /actionError[^\n]*role="alert"/)
})

test('inline notifications expose contextual recovery and accessible tone semantics', () => {
  assert.match(inline, /export type InlineNotificationTone = 'info' \| 'success' \| 'warning' \| 'error'/)
  assert.match(inline, /export type InlineNotificationAnnouncement = 'auto' \| 'polite' \| 'assertive' \| 'off'/)
  assert.match(inline, /announce\?: InlineNotificationAnnouncement/)
  assert.match(inline, /announce === 'off'/)
  assert.match(inline, /actionLabel/)
  assert.match(styles, /--faros-ui-version: 5/)
  assert.match(styles, /safe-area-inset-bottom/)
  assert.match(styles, /safe-area-inset-left/)
  assert.match(styles, /min-width: 44px/)
  assert.match(styles, /--k-toast-bottom-offset/)
  assert.match(styles, /pointer: coarse/)
  assert.match(styles, /\.k-toast-host\s*\{/)
  assert.match(styles, /\.k-inline-notification\s*\{/)
  assert.doesNotMatch(styles, /\.k-toast-move \{ transition: all /)
  assert.match(styles, /@media \(max-width: 420px\)/)
})
