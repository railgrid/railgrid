<!--
  CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
  under providers/*/portal/src/portalkit/; edit here and run
  `make sync-portalkit`.

  InlineNotification is for contextual state that belongs beside the failed
  operation. Toasts remain for transient success and context-free feedback.
-->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { Check, Info, TriangleAlert, X } from 'lucide-vue-next'
import { ensureRailgridUIStyles } from '../portalkit/styles'

export type InlineNotificationTone = 'info' | 'success' | 'warning' | 'error'
export type InlineNotificationAnnouncement = 'auto' | 'polite' | 'assertive' | 'off'

const props = withDefaults(defineProps<{
  tone?: InlineNotificationTone
  title?: string
  message?: string
  actionLabel?: string
  /** Accessible in-flight action. Falls back to the visible action label. */
  actionBusyLabel?: string
  actionBusy?: boolean
  /** Select the live channel; `off` leaves the notification silent. */
  announce?: InlineNotificationAnnouncement
  dismissible?: boolean
  dismissLabel?: string
}>(), {
  tone: 'info',
  title: '',
  message: '',
  actionLabel: '',
  actionBusyLabel: '',
  actionBusy: false,
  announce: 'auto',
  dismissible: false,
  dismissLabel: 'Dismiss notification',
})

const emit = defineEmits<{
  action: []
  dismiss: []
}>()

ensureRailgridUIStyles()

const assertive = () => props.tone === 'error'
const liveRole = () => {
  if (props.announce === 'off') return undefined
  return props.announce === 'assertive' || (props.announce === 'auto' && assertive()) ? 'alert' : 'status'
}
const liveMode = () => {
  if (props.announce === 'off') return undefined
  return liveRole() === 'alert' ? 'assertive' : 'polite'
}
const actionButtonLabel = computed(() => props.actionBusy
  ? props.actionBusyLabel || `${props.actionLabel}…`
  : props.actionLabel)
const actionRequested = ref(false)
function requestAction() {
  if (props.actionBusy || actionRequested.value) return
  actionRequested.value = true
  emit('action')
  // Allow synchronous actions to run again, while blocking duplicate events
  // until an async caller's busy prop has had a chance to arrive.
  void nextTick(() => { if (!props.actionBusy) actionRequested.value = false })
}
watch(() => props.actionBusy, busy => { if (!busy) actionRequested.value = false })
</script>

<template>
  <div
    class="k-inline-notification"
    :class="`k-inline-notification--${tone}`"
    :role="liveRole()"
    :aria-live="liveMode()"
    aria-atomic="true"
  >
    <Check v-if="tone === 'success'" class="k-inline-notification__icon" :stroke-width="2.25" aria-hidden="true" />
    <TriangleAlert v-else-if="tone === 'warning'" class="k-inline-notification__icon" :stroke-width="2" aria-hidden="true" />
    <X v-else-if="tone === 'error'" class="k-inline-notification__icon" :stroke-width="2.25" aria-hidden="true" />
    <Info v-else class="k-inline-notification__icon" :stroke-width="2" aria-hidden="true" />
    <div class="k-inline-notification__body">
      <strong v-if="title" class="k-inline-notification__title">{{ title }}</strong>
      <div v-if="message || $slots.default" class="k-inline-notification__message"><slot>{{ message }}</slot></div>
    </div>
    <button
      v-if="actionLabel"
      type="button"
      class="k-inline-notification__action"
      :aria-disabled="actionBusy ? 'true' : undefined"
      :aria-label="actionButtonLabel"
      :aria-busy="actionBusy ? 'true' : undefined"
      @click="requestAction"
    >
      {{ actionButtonLabel }}
    </button>
    <button
      v-if="dismissible"
      type="button"
      class="k-inline-notification__dismiss"
      :aria-label="dismissLabel"
      @click="emit('dismiss')"
    >
      <X :stroke-width="2" aria-hidden="true" />
    </button>
  </div>
</template>
