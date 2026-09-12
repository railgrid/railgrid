// CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
// under providers/*/portal/src/portalkit/; edit here and run
// `make sync-portalkit`.
//
// Vue PortalKit toast transport. Vue providers are independently bundled, so
// their modules cannot share a Vue ref directly. The global layer below owns
// host arbitration and the live command ledger only; the active ToastHost owns
// its reactive priority queue, timers, pausing, and visual lifecycle.

export const TOAST_TRANSPORT_VERSION = 1
export const TOAST_TRANSPORT_EVENT = 'faros:portalkit:toast'

export type ToastKind = 'ok' | 'info' | 'warning' | 'error'
export type ToastTone = ToastKind
export type ToastDismissReason = 'timeout' | 'dismiss' | 'action' | 'clear' | 'replaced'
export type ToastAnnouncement = 'auto' | 'polite' | 'assertive' | 'off'
export type ToastDuration = number | 'persistent'
export type ToastId = number

/** Host-owned state that must survive queue updates and host takeover. */
export interface ToastRuntimeState {
  actionBusy: boolean
  actionError?: string | null
  duration?: number | null
  persistent?: boolean
  remainingMs?: number
}

export interface ToastRuntimeUpdate {
  actionBusy?: boolean
  actionError?: string | null
  duration?: number | null
  persistent?: boolean
  remainingMs?: number
}

export interface ToastAction {
  label: string
  run: () => void | Promise<void>
  closeOnSuccess?: boolean
}

export interface ToastOptions {
  action?: ToastAction
  /** Finite durations are clamped to five seconds for non-persistent notices. */
  duration?: ToastDuration
  /** Dedupe and clear operations are isolated to this scope. */
  scope?: string
  dedupeKey?: string
  /** Source is rendered as a small technical provenance label. */
  source?: string
  announcement?: ToastAnnouncement
  onDismiss?: (reason: ToastDismissReason) => void
}

/** Full object input retained for callers that prefer one argument. */
export interface ToastInput extends ToastOptions {
  message: string
  kind?: ToastKind
}

/** A command is safe to keep in the handoff ledger; callbacks never enter DOM. */
export interface ToastCommand {
  id: ToastId
  kind: ToastKind
  title: string
  message: string
  persistent: boolean
  duration: number | null
  priority: number
  scope: string
  dedupeKey?: string
  source: string
  announcement: ToastAnnouncement
  hasAction: boolean
  actionLabel?: string
  action?: ToastAction
  closeOnSuccess: boolean
  dismissLabel: string
  focusOrigin: HTMLElement | null
  runtime: ToastRuntimeState
  onDismiss?: (reason: ToastDismissReason) => void
}

export interface ToastView {
  id: ToastId
  kind: ToastKind
  title: string
  message: string
  persistent: boolean
  duration: number | null
  priority: number
  scope: string
  dedupeKey?: string
  source: string
  announcement: ToastAnnouncement
  hasAction: boolean
  actionLabel?: string
  actionBusy: boolean
  actionError?: string
  closeOnSuccess: boolean
  dismissLabel: string
  focusOrigin: HTMLElement | null
}

export type ToastHostRole = 'primary' | 'fallback'
export interface ToastHostEntryState {
  id: ToastId
  visible: boolean
  remainingMs?: number
  actionBusy: boolean
  actionError?: string
  hovered: boolean
  focused: boolean
}

export interface ToastHostState {
  focusedToastID: ToastId | null
  entries: readonly ToastHostEntryState[]
}

export type ToastHostListener = (
  commands: readonly ToastCommand[],
  active: boolean,
  handoff?: ToastHostState,
) => void

interface ToastTransportDetail {
  version: number
  operation: 'enqueue' | 'update' | 'dismiss' | 'clear'
  operationID: string
  source: string
  id?: ToastId
  reason?: ToastDismissReason
  scope?: string
  command?: ToastCommand
}

interface HostRegistration {
  role: ToastHostRole
  listener: ToastHostListener
  stateProvider?: () => ToastHostState | null
}

interface ToastBridge {
  nextID: number
  nextOperation: number
  commands: Map<number, ToastCommand>
  listeners: Set<(commands: readonly ToastCommand[]) => void>
  hosts: Map<symbol, HostRegistration>
  handoff: ToastHostState | null
  seenOperations: Set<string>
  transportDocument: Document | null
  transportListener: ((event: Event) => void) | null
}

type ToastGlobal = typeof globalThis & { [key: symbol]: unknown }
const toastGlobal = globalThis as ToastGlobal
const BRIDGE_KEY = Symbol.for('faros.portalkit.vue.toast.bridge.v1')
const SOURCE_ID = `vue-toast-${Math.random().toString(36).slice(2)}`
const MIN_DURATION = 5000
const MAX_SEEN_OPERATIONS = 256
const DEFAULT_DURATION: Record<ToastKind, number> = {
  ok: 5000,
  info: 6000,
  warning: 0,
  error: 0,
}
const DEFAULT_PRIORITY: Record<ToastKind, number> = {
  ok: 10,
  info: 20,
  warning: 80,
  error: 100,
}

function getBridge(): ToastBridge {
  const current = toastGlobal[BRIDGE_KEY]
  if (current) return current as ToastBridge
  const created: ToastBridge = {
    nextID: 0,
    nextOperation: 0,
    commands: new Map(),
    listeners: new Set(),
    hosts: new Map(),
    handoff: null,
    seenOperations: new Set(),
    transportDocument: null,
    transportListener: null,
  }
  toastGlobal[BRIDGE_KEY] = created
  return created
}

function currentDocument(): Document | null {
  return typeof document === 'undefined' ? null : document
}

function activeHostToken(bridge: ToastBridge): symbol | null {
  // A primary shell host always wins over fallback hosts. Insertion order
  // makes several direct embeds deterministic if a page contains more than one.
  for (const [token, registration] of bridge.hosts) {
    if (registration.role === 'primary') return token
  }
  for (const [token] of bridge.hosts) return token
  return null
}

function currentHandoff(bridge: ToastBridge, state: ToastHostState | null | undefined): ToastHostState | null {
  if (!state) return null
  const entries = state.entries.filter(entry => bridge.commands.has(entry.id))
  if (!entries.length) return null
  const focusedToastID = state.focusedToastID !== null && bridge.commands.has(state.focusedToastID)
    ? state.focusedToastID
    : null
  return { focusedToastID, entries }
}

function commandSnapshot(bridge = getBridge()): readonly ToastCommand[] {
  return [...bridge.commands.values()].sort((left, right) =>
    right.priority - left.priority || left.id - right.id,
  )
}

function rememberOperation(bridge: ToastBridge, operationID: string): boolean {
  if (bridge.seenOperations.has(operationID)) return false
  bridge.seenOperations.add(operationID)
  // Operation IDs only protect against replay while a document is alive. Keep
  // a bounded insertion-ordered window so long-lived shells cannot leak one
  // entry per toast operation forever.
  while (bridge.seenOperations.size > MAX_SEEN_OPERATIONS) {
    const oldest = bridge.seenOperations.values().next().value
    if (typeof oldest !== 'string') break
    bridge.seenOperations.delete(oldest)
  }
  return true
}

function notify(bridge = getBridge()): void {
  const next = commandSnapshot(bridge)
  for (const listener of bridge.listeners) {
    try {
      listener(next)
    } catch {
      // A stale consumer must not prevent the active shell host from updating.
    }
  }
  const active = activeHostToken(bridge)
  const handoff = bridge.handoff
  let handoffDelivered = false
  for (const [token, registration] of bridge.hosts) {
    const isActive = active === token
    if (isActive && handoff) handoffDelivered = true
    try {
      registration.listener(isActive ? next : [], isActive, isActive ? handoff ?? undefined : undefined)
    } catch {
      // Host teardown races should not break the queue or another host.
    }
  }
  if (handoffDelivered) bridge.handoff = null
}

function publish(
  operation: ToastTransportDetail['operation'],
  details: Omit<ToastTransportDetail, 'version' | 'operation' | 'operationID' | 'source'> = {},
): void {
  const bridge = getBridge()
  const operationID = `${SOURCE_ID}:${++bridge.nextOperation}`
  rememberOperation(bridge, operationID)
  notify(bridge)

  const doc = currentDocument()
  if (!doc || typeof CustomEvent === 'undefined') return
  const detail: ToastTransportDetail = {
    version: TOAST_TRANSPORT_VERSION,
    operation,
    operationID,
    source: SOURCE_ID,
    ...details,
  }
  doc.dispatchEvent(new CustomEvent<ToastTransportDetail>(TOAST_TRANSPORT_EVENT, { detail }))
}

function matchesScope(command: ToastCommand, scope: string | undefined): boolean {
  return scope === undefined || command.scope === scope
}

function normaliseKind(options: ToastInput): ToastKind {
  return options.kind ?? 'info'
}

function normaliseMessage(message: string | undefined): string {
  const value = String(message ?? '').trim()
  return value || 'Notification'
}

function normaliseDuration(options: ToastInput, kind: ToastKind): { persistent: boolean; duration: number | null } {
  // Destructive/error context and a recovery action must stay available until
  // the user dismisses them. Success and informational feedback cannot vanish
  // before a user has had five seconds to perceive it.
  const requested = typeof options.duration === 'number' && Number.isFinite(options.duration)
    ? options.duration
    : undefined
  const persistent = options.duration === 'persistent' || Boolean(options.action) ||
    (requested === undefined && (kind === 'warning' || kind === 'error'))
  if (persistent) return { persistent: true, duration: null }
  return { persistent: false, duration: Math.max(MIN_DURATION, requested ?? DEFAULT_DURATION[kind]) }
}

function focusOrigin(): HTMLElement | null {
  const doc = currentDocument()
  return doc?.activeElement instanceof HTMLElement ? doc.activeElement : null
}

function findDedupe(
  bridge: ToastBridge,
  scope: string,
  dedupeKey: string | undefined,
): ToastCommand | undefined {
  if (!dedupeKey) return undefined
  return [...bridge.commands.values()].find(command =>
    command.scope === scope && command.dedupeKey === dedupeKey,
  )
}

function createCommand(id: ToastId, options: ToastInput, kind: ToastKind): ToastCommand {
  const timing = normaliseDuration(options, kind)
  const source = options.source?.trim() || ''
  return {
    id,
    kind,
    title: '',
    message: normaliseMessage(options.message),
    persistent: timing.persistent,
    duration: timing.duration,
    priority: DEFAULT_PRIORITY[kind],
    scope: options.scope ?? 'global',
    ...(options.dedupeKey ? { dedupeKey: options.dedupeKey } : {}),
    source,
    announcement: options.announcement ?? 'auto',
    hasAction: Boolean(options.action),
    ...(options.action ? { actionLabel: options.action.label, action: options.action } : {}),
    closeOnSuccess: options.action?.closeOnSuccess !== false,
    dismissLabel: 'Dismiss notification',
    focusOrigin: focusOrigin(),
    runtime: { actionBusy: false },
    ...(options.onDismiss ? { onDismiss: options.onDismiss } : {}),
  }
}

function removeCommand(command: ToastCommand, reason: ToastDismissReason, transport: boolean): void {
  const bridge = getBridge()
  if (bridge.commands.get(command.id) !== command) return
  bridge.commands.delete(command.id)
  try {
    command.onDismiss?.(reason)
  } catch {
    // Dismissal callbacks are observers; one bad callback cannot strand queue state.
  }
  if (transport) publish('dismiss', { id: command.id, reason })
  else notify(bridge)
}

function applyTransport(detail: ToastTransportDetail): void {
  if (detail.version !== TOAST_TRANSPORT_VERSION || !detail.operationID) return
  const bridge = getBridge()
  if (!rememberOperation(bridge, detail.operationID)) return

  if ((detail.operation === 'enqueue' || detail.operation === 'update') && detail.command) {
    bridge.nextID = Math.max(bridge.nextID, detail.command.id)
    if (detail.operation === 'enqueue') {
      if (bridge.commands.has(detail.command.id)) return
      bridge.commands.set(detail.command.id, detail.command)
    } else {
      const current = bridge.commands.get(detail.command.id)
      if (!current) return
      bridge.commands.set(detail.command.id, {
        ...current,
        ...detail.command,
        runtime: { ...current.runtime, ...detail.command.runtime },
      })
    }
    notify(bridge)
    return
  }
  if (detail.operation === 'dismiss' && detail.id !== undefined) {
    const command = bridge.commands.get(detail.id)
    if (command) removeCommand(command, detail.reason ?? 'dismiss', false)
    return
  }
  if (detail.operation === 'clear') {
    if (!detail.scope) return
    for (const command of [...bridge.commands.values()]) {
      if (command.scope === detail.scope) removeCommand(command, 'clear', false)
    }
  }
}

function ensureTransportListener(): void {
  const bridge = getBridge()
  const doc = currentDocument()
  if (!doc || bridge.transportDocument === doc) return
  if (bridge.transportDocument && bridge.transportListener) {
    bridge.transportDocument.removeEventListener(TOAST_TRANSPORT_EVENT, bridge.transportListener)
  }
  const listener = (event: Event) => {
    const detail = (event as CustomEvent<ToastTransportDetail>).detail
    if (!detail || detail.source === SOURCE_ID) return
    applyTransport(detail)
  }
  bridge.transportDocument = doc
  bridge.transportListener = listener
  doc.addEventListener(TOAST_TRANSPORT_EVENT, listener)
}

function enqueue(options: ToastInput): ToastId {
  ensureTransportListener()
  const bridge = getBridge()
  const kind = normaliseKind(options)
  const scope = options.scope ?? 'global'
  const deduped = findDedupe(bridge, scope, options.dedupeKey)
  if (deduped) {
    // Replacement is a true dismissal followed by a fresh enqueue. This
    // resets the host-owned timer and gives callers the new command identity;
    // the old callback observes exactly one `replaced` reason.
    removeCommand(deduped, 'replaced', true)
  }
  const command = createCommand(++bridge.nextID, options, kind)
  bridge.commands.set(command.id, command)
  publish('enqueue', { id: command.id, command })
  return command.id
}

function positionalOptions(actionOrOptions: ToastAction | ToastOptions | undefined): ToastOptions {
  if (actionOrOptions && 'run' in actionOrOptions) return { action: actionOrOptions }
  return actionOrOptions ?? {}
}

/** Legacy Vue-compatible overload: toast('ok', 'Saved', action). */
export function toast(kind: ToastKind, message: string, actionOrOptions?: ToastAction | ToastOptions): ToastId
export function toast(options: ToastInput): ToastId
export function toast(
  kindOrOptions: ToastKind | ToastInput,
  message?: string,
  actionOrOptions?: ToastAction | ToastOptions,
): ToastId {
  if (typeof kindOrOptions === 'string') {
    const options = positionalOptions(actionOrOptions)
    return enqueue({ kind: kindOrOptions, message: normaliseMessage(message), ...options })
  }
  return enqueue(kindOrOptions)
}

export function dismissToast(
  id: ToastId,
  scope?: string,
  reason: ToastDismissReason = 'dismiss',
): void {
  ensureTransportListener()
  const command = getBridge().commands.get(id)
  if (!command || !matchesScope(command, scope)) return
  removeCommand(command, reason, true)
}

export function clearToasts(scope: string): void {
  ensureTransportListener()
  const bridge = getBridge()
  const commands = [...bridge.commands.values()].filter(command => command.scope === scope)
  if (!commands.length) return
  for (const command of commands) removeCommand(command, 'clear', false)
  publish('clear', { scope })
}

/**
 * Persist host-local action/timer state in the shared command ledger. Async
 * actions may settle after their original host has been replaced, so callers
 * must address the command by ID rather than retaining a reactive entry.
 */
export function updateToastRuntime(id: ToastId, updates: ToastRuntimeUpdate): void {
  ensureTransportListener()
  const bridge = getBridge()
  const command = bridge.commands.get(id)
  if (!command) return

  const runtime = { ...command.runtime }
  let changed = false
  for (const key of ['actionBusy', 'actionError', 'duration', 'persistent', 'remainingMs'] as const) {
    if (!(key in updates)) continue
    const next = updates[key]
    if (Object.is(runtime[key], next)) continue
    runtime[key] = next as never
    changed = true
  }
  if (!changed) return

  const updated = { ...command, runtime }
  bridge.commands.set(id, updated)
  publish('update', { id, command: updated })
}

export function subscribeToasts(listener: (commands: readonly ToastCommand[]) => void): () => void {
  ensureTransportListener()
  const bridge = getBridge()
  bridge.listeners.add(listener)
  listener(commandSnapshot(bridge))
  return () => bridge.listeners.delete(listener)
}

/** Register a primary shell host or a dormant provider fallback host. */
export function registerToastHost(
  role: ToastHostRole,
  listener: ToastHostListener,
  stateProvider?: () => ToastHostState | null,
): () => void {
  ensureTransportListener()
  const bridge = getBridge()
  const token = Symbol(`toast-host:${role}`)
  const previousActive = activeHostToken(bridge)
  const previousRegistration = previousActive === null ? undefined : bridge.hosts.get(previousActive)
  const newHostTakesOwnership = previousActive === null ||
    (role === 'primary' && previousRegistration?.role === 'fallback')
  if (newHostTakesOwnership) {
    const previousState = previousRegistration
      ? previousRegistration.stateProvider?.()
      : bridge.handoff
    bridge.handoff = currentHandoff(bridge, previousState)
  }
  bridge.hosts.set(token, { role, listener, stateProvider })
  notify(bridge)
  return () => {
    const registration = bridge.hosts.get(token)
    if (!registration) return
    if (activeHostToken(bridge) === token) {
      bridge.handoff = currentHandoff(bridge, registration.stateProvider?.())
    }
    bridge.hosts.delete(token)
    notify(bridge)
  }
}

export interface ToastService {
  toast: ToastServiceToast
  dismissToast: (id: ToastId, reason?: ToastDismissReason) => void
  clearToasts: (scope?: string) => void
  subscribeToasts: typeof subscribeToasts
}

export interface ToastScope {
  scope?: string
  source?: string
}

export interface ToastServiceToast {
  (kind: ToastKind, message: string, actionOrOptions?: ToastAction | ToastOptions): ToastId
  (options: ToastInput): ToastId
}

/** Vue-friendly service access with optional source/scope defaults. */
export function useToast(defaults: ToastScope = {}): ToastService {
  const scopedToast: ToastServiceToast = (kindOrOptions: ToastKind | ToastInput, message?: string, actionOrOptions?: ToastAction | ToastOptions): ToastId => {
    if (typeof kindOrOptions === 'string') {
      const options = positionalOptions(actionOrOptions)
      return enqueue({
        kind: kindOrOptions,
        message: normaliseMessage(message),
        ...options,
        ...(options.scope === undefined && defaults.scope !== undefined ? { scope: defaults.scope } : {}),
        ...(options.source === undefined && defaults.source !== undefined ? { source: defaults.source } : {}),
      })
    }
    return enqueue({
      ...kindOrOptions,
      ...(kindOrOptions.scope === undefined && defaults.scope !== undefined ? { scope: defaults.scope } : {}),
      ...(kindOrOptions.source === undefined && defaults.source !== undefined ? { source: defaults.source } : {}),
    })
  }
  const scopedClear = (scope?: string): void => {
    const targetScope = scope ?? defaults.scope
    if (targetScope === undefined) return
    clearToasts(targetScope)
  }
  const scopedDismiss = (id: ToastId, reason: ToastDismissReason = 'dismiss'): void => dismissToast(id, defaults.scope, reason)
  return { toast: scopedToast, dismissToast: scopedDismiss, clearToasts: scopedClear, subscribeToasts }
}
