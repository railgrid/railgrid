<!-- CANONICAL SOURCE — edit here, then run make sync-portalkit. -->
<script setup lang="ts">
import { computed, nextTick } from 'vue'
import InlineNotification from './InlineNotification.vue'

const props = defineProps<{
  title: string
  message: string
  returnFocus?: HTMLElement | null
  retryLabel?: string
  retryHint?: string
}>()
const emit = defineEmits<{ dismiss: []; retry: [] }>()
const message = computed(() => {
  const detail = props.message.trim()
  if (props.retryLabel) return detail
  const instruction = props.retryHint ?? 'Try again using the row action.'
  if (!detail) return instruction
  return /[.!?]$/.test(detail) ? detail + ' ' + instruction : detail + '. ' + instruction
})

async function dismiss() {
  const focusTarget = props.returnFocus ?? (typeof document === 'undefined' ? null
    : document.activeElement?.closest('.k-resource-page')?.querySelector<HTMLElement>('.k-resource-page__title'))
  emit('dismiss')
  await nextTick()
  if (typeof document !== 'undefined' && document.activeElement === document.body) {
    focusTarget?.focus({ preventScroll: true })
  }
}
</script>

<template>
  <InlineNotification
    tone="error"
    :title="title"
    :message="message"
    :action-label="retryLabel"
    dismissible
    :dismiss-label="'Dismiss: ' + title"
    @dismiss="dismiss"
    @action="emit('retry')"
  />
</template>
