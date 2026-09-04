<!--
  CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
  under providers/*/portal/src/portalkit/; edit here and run
  `make sync-portalkit`.

  Mount one `owner="primary"` instance in the shell. Provider bundles may mount
  one `owner="fallback"` instance as a dormant fallback for direct embeds;
  the global toast bridge guarantees that only the active host renders.
-->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { Check, Info, Loader2, TriangleAlert, X } from 'lucide-vue-next'
import { ensureFarosUIStyles } from '../portalkit/styles'
import {
  dismissToast,
  registerToastHost,
  updateToastRuntime,
  type ToastAction,
  type ToastCommand,
  type ToastDismissReason,
  type ToastHostRole,
  type ToastHostState,
  type ToastView,
} from './toast'

const props = withDefaults(defineProps<{
  owner?: ToastHostRole
}>(), {
  owner: 'primary',
})

ensureFarosUIStyles()

interface ToastEntry extends ToastView {
  action?: ToastAction
  onDismiss?: (reason: ToastDismissReason) => void
  timer?: ReturnType<typeof setTimeout>
  timerToken?: number
  expiresAt?: number
  remainingMs?: number
  hovered: boolean
  focused: boolean
  visible: boolean
}

const root = ref<HTMLElement | null>(null)
const entries = ref<ToastEntry[]>([])
const active = ref(false)
const focusedToastID = ref<number | null>(null)
const focusOrigins = new Map<number, HTMLElement | null>()
const MAX_VISIBLE = 1
let timerSequence = 0
let unregister: (() => void) | null = null

const activeToast = computed(() => entries.value.find(entry => entry.visible) ?? null)
const assertive = (toast: ToastView): boolean => {
  if (toast.announcement === 'off') return false
  // An action failure is a recovery-critical update. Keep it on the mounted
  // alert channel even when the original notice was informational.
  if (toast.actionError) return true
  if (toast.announcement === 'polite') return false
  if (toast.announcement === 'assertive') return true
  return toast.kind === 'error' || Boolean(toast.actionError)
}
const announcementFor = (toast: ToastView): string => {
  if (toast.announcement === 'off') return ''
  const title = toast.title ? `${toast.title}: ` : ''
  const action = toast.actionLabel ? ` ${toast.actionLabel} available.` : ''
  const actionError = toast.actionError ? ` ${toast.actionError}` : ''
  return `${title}${toast.message}${action}${actionError}`
}
const statusAnnouncement = computed(() => {
  const toast = activeToast.value
  return toast && !assertive(toast) ? announcementFor(toast) : ''
})
const alertAnnouncement = computed(() => {
  const toast = activeToast.value
  return toast && assertive(toast) ? announcementFor(toast) : ''
})

function clearTimer(entry: ToastEntry): void {
  if (entry.timer !== undefined) {
    clearTimeout(entry.timer)
    entry.timer = undefined
  }
  entry.timerToken = undefined
  entry.expiresAt = undefined
}

function armTimer(entry: ToastEntry): void {
  clearTimer(entry)
  if (!active.value || !entry.visible || entry.persistent || entry.duration === null || document.hidden || entry.hovered || entry.focused) return
  const delay = Math.max(1, entry.remainingMs ?? entry.duration)
  entry.remainingMs = delay
  entry.expiresAt = Date.now() + delay
  const timerToken = ++timerSequence
  entry.timerToken = timerToken
  const timer = setTimeout(() => {
    const current = entries.value.find(current => current.id === entry.id)
    if (!current || current.timerToken !== timerToken) return
    dismissToast(entry.id, undefined, 'timeout')
  }, delay)
  entry.timer = timer
}

function pauseEntry(entry: ToastEntry): void {
  if (!entry.visible || entry.persistent || entry.duration === null) return
  if (entry.timer !== undefined && entry.expiresAt !== undefined) {
    entry.remainingMs = Math.max(1, entry.expiresAt - Date.now())
    clearTimer(entry)
  }
}

function resumeEntry(entry: ToastEntry): void {
  armTimer(entry)
}

function updatePauseState(entry: ToastEntry): void {
  if (document.hidden || entry.hovered || entry.focused) pauseEntry(entry)
  else resumeEntry(entry)
}

function highestPending(): ToastEntry | undefined {
  return entries.value
    .filter(entry => !entry.visible)
    .sort((left, right) => right.priority - left.priority || left.id - right.id)[0]
}

function rebalance(): void {
  const visibleEntries = entries.value.filter(entry => entry.visible)
  const visible = visibleEntries[0]
  for (const extra of visibleEntries.slice(MAX_VISIBLE)) extra.visible = false
  if (!visible) {
    const next = highestPending()
    if (next) {
      next.visible = true
      armTimer(next)
    }
    return
  }
  const next = highestPending()
  if (next && next.priority > visible.priority) {
    pauseEntry(visible)
    visible.visible = false
    visible.hovered = false
    visible.focused = false
    next.visible = true
    armTimer(next)
  }
}

function commandToEntry(command: ToastCommand, previous?: ToastEntry, handoff?: ToastHostState): ToastEntry {
  const transferred = handoff?.entries.find(entry => entry.id === command.id)
  const runtime = command.runtime ?? { actionBusy: false }
  const duration = runtime.duration !== undefined ? runtime.duration : command.duration
  const persistent = runtime.persistent ?? command.persistent
  const actionError = Object.prototype.hasOwnProperty.call(runtime, 'actionError')
    ? runtime.actionError ?? undefined
    : transferred?.actionError ?? previous?.actionError
  return {
    ...command,
    persistent,
    duration,
    actionBusy: runtime.actionBusy ?? transferred?.actionBusy ?? previous?.actionBusy ?? false,
    ...(actionError ? { actionError } : {}),
    action: command.action,
    onDismiss: command.onDismiss,
    visible: transferred?.visible ?? previous?.visible ?? false,
    hovered: transferred?.visible ? transferred.hovered : previous?.visible ? previous.hovered : false,
    focused: transferred?.visible ? transferred.focused : previous?.visible ? previous.focused : false,
    ...((runtime.remainingMs ?? transferred?.remainingMs ?? previous?.remainingMs) !== undefined
      ? { remainingMs: runtime.remainingMs ?? transferred?.remainingMs ?? previous?.remainingMs }
      : {}),
    ...(previous?.timer !== undefined ? { timer: previous.timer } : {}),
    ...(previous?.timerToken !== undefined ? { timerToken: previous.timerToken } : {}),
    ...(previous?.expiresAt !== undefined ? { expiresAt: previous.expiresAt } : {}),
  }
}

function onCommands(commands: readonly ToastCommand[], isActive: boolean, handoff?: ToastHostState): void {
  const previous = entries.value
  const nextIDs = new Set(commands.map(command => command.id))
  const previousFocusedID = handoff ? handoff.focusedToastID : focusedToastID.value
  const previousFocusedOrigin = previousFocusedID === null ? undefined : focusOrigins.get(previousFocusedID)
  for (const entry of previous) {
    if (!nextIDs.has(entry.id)) {
      clearTimer(entry)
      focusOrigins.delete(entry.id)
    }
  }
  if (!isActive) {
    for (const entry of previous) clearTimer(entry)
    entries.value = []
    active.value = false
    focusedToastID.value = null
    focusOrigins.clear()
    return
  }

  const previousVisible = previous.find(entry => entry.visible)
  entries.value = commands.map(command => commandToEntry(
    command,
    previous.find(entry => entry.id === command.id),
    handoff,
  ))
  for (const command of commands) focusOrigins.set(command.id, command.focusOrigin)
  if (handoff) focusedToastID.value = handoff.focusedToastID
  active.value = true
  rebalance()

  const nextVisible = activeToast.value
  if (focusedToastID.value !== null && previousVisible?.id !== nextVisible?.id) {
    const origin = focusOrigins.get(previousVisible?.id ?? focusedToastID.value) ?? previousFocusedOrigin
    focusedToastID.value = null
    void nextTick(() => {
      const nextControl = nextVisible && focusControl(nextVisible.id)
      if (nextControl) nextControl.focus()
      else if (origin?.isConnected) origin.focus()
    })
  }
}

function focusControl(id: number): HTMLElement | null {
  const card = root.value?.querySelector<HTMLElement>(`[data-toast-id="${id}"]`)
  if (!card) return null
  return card.querySelector<HTMLElement>('.k-toast__action:not(:disabled), .k-toast__dismiss') ?? card
}

function rememberFocus(id: number): void {
  const current = document.activeElement
  const card = root.value?.querySelector<HTMLElement>(`[data-toast-id="${id}"]`)
  if (current instanceof HTMLElement && card?.contains(current)) focusedToastID.value = id
  const item = entries.value.find(entry => entry.id === id)
  if (item) focusOrigins.set(id, item.focusOrigin)
}

function dismiss(id: number): void {
  rememberFocus(id)
  dismissToast(id)
}

async function runAction(entry: ToastEntry): Promise<void> {
  const id = entry.id
  const action = entry.action
  const current = entries.value.find(candidate => candidate.id === id)
  if (!action || !current || current.actionBusy) return
  rememberFocus(id)
  updateToastRuntime(id, { actionBusy: true, actionError: null })
  try {
    await action.run()
    if (entry.closeOnSuccess) {
      dismissToast(id, undefined, 'action')
    } else {
      updateToastRuntime(id, { actionBusy: false, actionError: null })
    }
  } catch {
    // Keep thrown details out of the UI. Provider errors can contain secrets or
    // implementation paths; the toast only promises a safe retry affordance.
    if (!entries.value.some(candidate => candidate.id === id)) {
      updateToastRuntime(id, {
        actionBusy: false,
        actionError: 'Action failed. Try again.',
        persistent: true,
        duration: null,
      })
      return
    }
    updateToastRuntime(id, {
      actionBusy: false,
      actionError: 'Action failed. Try again.',
      persistent: true,
      duration: null,
    })
  }
}

function remainingMs(entry: ToastEntry): number | undefined {
  if (!entry.visible || entry.persistent || entry.duration === null) return entry.remainingMs
  if (entry.timer !== undefined && entry.expiresAt !== undefined) {
    return Math.max(1, entry.expiresAt - Date.now())
  }
  return entry.remainingMs
}

function focusedEntryID(): number | null {
  const current = document.activeElement
  if (!(current instanceof HTMLElement)) return null
  const card = current.closest<HTMLElement>('[data-toast-id]')
  const id = card?.dataset.toastId
  if (!id) return null
  const numericID = Number(id)
  return Number.isFinite(numericID) ? numericID : null
}

function captureHandoff(): ToastHostState | null {
  if (!active.value) return null
  const focusedID = focusedToastID.value ?? focusedEntryID()
  const states = entries.value.map(entry => {
    const remaining = remainingMs(entry)
    return {
      id: entry.id,
      visible: entry.visible,
      ...(remaining !== undefined ? { remainingMs: remaining } : {}),
      actionBusy: entry.actionBusy,
      ...(entry.actionError ? { actionError: entry.actionError } : {}),
      hovered: entry.hovered,
      focused: entry.focused,
    }
  })
  return {
    focusedToastID: focusedID,
    entries: states,
  }
}

function entryForEvent(event: Event): ToastEntry | null {
  const target = event.currentTarget
  if (!(target instanceof HTMLElement)) return null
  const card = target.closest<HTMLElement>('[data-toast-id]')
  const id = card?.dataset.toastId
  if (!id) return null
  return entries.value.find(entry => String(entry.id) === id) ?? null
}

function handleMouseEnter(event: MouseEvent): void {
  const entry = entryForEvent(event)
  if (!entry) return
  entry.hovered = true
  updatePauseState(entry)
}

function handleMouseLeave(event: MouseEvent): void {
  const entry = entryForEvent(event)
  if (!entry) return
  entry.hovered = false
  updatePauseState(entry)
}

function handleFocusIn(event: FocusEvent): void {
  const entry = entryForEvent(event)
  if (!entry) return
  focusedToastID.value = entry.id
  entry.focused = true
  updatePauseState(entry)
}

function handleFocusOut(event: FocusEvent): void {
  const target = event.currentTarget
  if (!(target instanceof HTMLElement)) return
  const related = event.relatedTarget
  if (related instanceof Node && target.contains(related)) return
  const entry = entryForEvent(event)
  if (!entry) return
  if (focusedToastID.value === entry.id) focusedToastID.value = null
  entry.focused = false
  updatePauseState(entry)
}

function handleAction(event: MouseEvent): void {
  const entry = entryForEvent(event)
  if (entry) void runAction(entry)
}

function handleDismiss(event: MouseEvent): void {
  const entry = entryForEvent(event)
  if (entry) dismiss(entry.id)
}

function keydown(event: KeyboardEvent): void {
  if (event.key !== 'Escape' || event.defaultPrevented || !activeToast.value) return
  if (!root.value?.contains(document.activeElement)) return
  event.preventDefault()
  dismiss(activeToast.value.id)
}

function visibilityChanged(): void {
  const entry = activeToast.value
  if (!entry) return
  if (document.hidden) pauseEntry(entry)
  else updatePauseState(entry)
}

onMounted(() => {
  document.addEventListener('visibilitychange', visibilityChanged)
  document.addEventListener('keydown', keydown)
  unregister = registerToastHost(props.owner, onCommands, captureHandoff)
})

onBeforeUnmount(() => {
  document.removeEventListener('visibilitychange', visibilityChanged)
  document.removeEventListener('keydown', keydown)
  unregister?.()
  unregister = null
  for (const entry of entries.value) clearTimer(entry)
})
</script>

<template>
  <Teleport to="body">
    <div
      ref="root"
      class="k-toast-host"
      :class="{ 'k-toast-host--dormant': !active }"
      :data-faros-toast-host="props.owner"
      :data-active="active ? 'true' : 'false'"
      :aria-hidden="active ? undefined : 'true'"
    >
      <div class="k-toast-host__visual" aria-label="Notifications">
        <Transition name="k-toast">
          <article
            v-if="activeToast"
            :id="`k-vue-toast-${activeToast.id}`"
            class="k-toast"
            :class="`k-toast--${activeToast.kind}`"
            :data-toast-id="activeToast.id"
            tabindex="-1"
            role="group"
            :aria-label="activeToast.title || activeToast.message"
            @mouseenter="handleMouseEnter"
            @mouseleave="handleMouseLeave"
            @focusin="handleFocusIn"
            @focusout="handleFocusOut"
          >
            <Check v-if="activeToast.kind === 'ok'" class="k-toast__icon" :stroke-width="2.25" aria-hidden="true" />
            <TriangleAlert v-else-if="activeToast.kind === 'warning'" class="k-toast__icon" :stroke-width="2" aria-hidden="true" />
            <X v-else-if="activeToast.kind === 'error'" class="k-toast__icon" :stroke-width="2.25" aria-hidden="true" />
            <Info v-else class="k-toast__icon" :stroke-width="2" aria-hidden="true" />

            <div class="k-toast__body">
              <strong v-if="activeToast.title" class="k-toast__title">{{ activeToast.title }}</strong>
              <span class="k-toast__message">{{ activeToast.message }}</span>
              <span v-if="activeToast.source" class="k-toast__source">{{ activeToast.source }}</span>
              <span v-if="activeToast.actionError" class="k-toast__action-error">{{ activeToast.actionError }}</span>
            </div>

            <button
              v-if="activeToast.hasAction"
              type="button"
              class="k-toast__action"
              :disabled="activeToast.actionBusy"
              :aria-busy="activeToast.actionBusy ? 'true' : undefined"
              @click.stop="handleAction"
            >
              <Loader2 v-if="activeToast.actionBusy" class="k-toast__action-spinner" :stroke-width="2" aria-hidden="true" />
              {{ activeToast.actionBusy ? 'Working…' : activeToast.actionLabel }}
            </button>
            <button
              type="button"
              class="k-toast__dismiss"
              :aria-label="activeToast.dismissLabel"
              @click.stop="handleDismiss"
            >
              <X :stroke-width="2" aria-hidden="true" />
            </button>
          </article>
        </Transition>
      </div>

      <!-- Keep both live regions mounted from first paint. Visual cards are
           announced once here, avoiding duplicate status/alert announcements. -->
      <div class="k-toast-host__channel k-toast-host__channel--status" role="status" aria-live="polite" aria-atomic="true">{{ statusAnnouncement }}</div>
      <div class="k-toast-host__channel k-toast-host__channel--alert" role="alert" aria-live="assertive" aria-atomic="true">{{ alertAnnouncement }}</div>
    </div>
  </Teleport>
</template>
