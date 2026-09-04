<!--
  CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
  under providers/*/portal/src/portalkit/; edit here and run
  `make sync-portalkit`.

  InlineNotification is for contextual state that belongs beside the failed
  operation. Toasts remain for transient success and context-free feedback.
-->
<script setup lang="ts">
import { Check, Info, TriangleAlert, X } from 'lucide-vue-next'
import { ensureFarosUIStyles } from '../portalkit/styles'

export type InlineNotificationTone = 'info' | 'success' | 'warning' | 'error'
export type InlineNotificationAnnouncement = 'auto' | 'polite' | 'assertive' | 'off'

const props = withDefaults(defineProps<{
  tone?: InlineNotificationTone
  title?: string
  message?: string
  actionLabel?: string
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
  actionBusy: false,
  announce: 'auto',
  dismissible: false,
  dismissLabel: 'Dismiss notification',
})

const emit = defineEmits<{
  action: []
  dismiss: []
}>()

ensureFarosUIStyles()

const assertive = () => props.tone === 'error'
const liveRole = () => {
  if (props.announce === 'off') return undefined
  return props.announce === 'assertive' || (props.announce === 'auto' && assertive()) ? 'alert' : 'status'
}
const liveMode = () => {
  if (props.announce === 'off') return undefined
  return liveRole() === 'alert' ? 'assertive' : 'polite'
}
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
      <span v-if="message || $slots.default" class="k-inline-notification__message"><slot>{{ message }}</slot></span>
    </div>
    <button
      v-if="actionLabel"
      type="button"
      class="k-inline-notification__action"
      :disabled="actionBusy"
      :aria-busy="actionBusy ? 'true' : undefined"
      @click="emit('action')"
    >
      {{ actionBusy ? 'Working…' : actionLabel }}
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
