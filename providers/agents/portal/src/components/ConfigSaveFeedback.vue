<script setup lang="ts">
import { computed } from 'vue'
import { Check, Loader2, TriangleAlert } from 'lucide-vue-next'

export type ConfigSaveStatus = 'idle' | 'dirty' | 'pending' | 'saved' | 'error'

const props = withDefaults(defineProps<{
  status: ConfigSaveStatus
  action: string
  newerEdits?: boolean
  id?: string
  error?: string
}>(), { newerEdits: false, id: undefined, error: undefined })

const tone = computed(() => {
  if (props.status === 'error') return 'k-inline-notification--error'
  if (props.status === 'saved') return 'k-inline-notification--success'
  if (props.status === 'dirty') return 'k-inline-notification--warning'
  return 'k-inline-notification--info'
})

const message = computed(() => {
  switch (props.status) {
    case 'dirty': return 'Unsaved changes'
    case 'pending': return props.newerEdits ? `Saving ${props.action}… Newer edits remain unsaved.` : `Saving ${props.action}…`
    case 'saved': return `${props.action[0]?.toUpperCase() || ''}${props.action.slice(1)} saved.`
    case 'error': return props.error || `Could not save ${props.action}. Try again.`
    default: return ''
  }
})
</script>

<template>
  <div
    v-if="status !== 'idle'"
    :id="id"
    class="k-inline-notification agents-config-save-feedback"
    :class="tone"
    :role="status === 'error' ? 'alert' : 'status'"
    :aria-live="status === 'error' ? 'assertive' : 'polite'"
    aria-atomic="true"
    :data-config-save-status="status"
  >
    <span class="k-inline-notification__icon" aria-hidden="true">
      <Loader2 v-if="status === 'pending'" class="k-spin" :stroke-width="1.75" />
      <TriangleAlert v-else-if="status === 'error'" :stroke-width="1.75" />
      <TriangleAlert v-else-if="status === 'dirty'" :stroke-width="1.75" />
      <Check v-else :stroke-width="1.75" />
    </span>
    <span class="k-inline-notification__body"><span class="k-inline-notification__message">{{ message }}</span></span>
  </div>
</template>
