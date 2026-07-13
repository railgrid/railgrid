<script setup lang="ts">
import { computed } from 'vue'
import { CheckCircle, Clock, AlertTriangle, Lock } from 'lucide-vue-next'
import type { ProjectCheckpoint } from '../types'

const props = defineProps<{ checkpoint: ProjectCheckpoint }>()
const emit = defineEmits<{ (e: 'act', checkpoint: ProjectCheckpoint): void }>()

const config = computed(() => {
  switch (props.checkpoint.state) {
    case 'done':
      return { bg: 'bg-success-subtle', text: 'text-success', icon: CheckCircle, dot: 'bg-success' }
    case 'error':
      return { bg: 'bg-danger-subtle', text: 'text-danger', icon: AlertTriangle, dot: 'bg-danger' }
    case 'blocked':
      return { bg: 'bg-surface-overlay', text: 'text-text-muted', icon: Lock, dot: 'bg-text-muted' }
    default: // pending
      return { bg: 'bg-warning-subtle', text: 'text-warning', icon: Clock, dot: 'bg-warning' }
  }
})

// A checkpoint is actionable when it is not yet done and has a remediation.
const actionable = computed(
  () => props.checkpoint.state !== 'done' && !!props.checkpoint.remediation,
)

const title = computed(() => {
  const parts = [props.checkpoint.reason]
  const message = props.checkpoint.remediation?.message
  if (message && message !== props.checkpoint.reason) parts.push(message)
  return parts.filter(Boolean).join(' — ')
})
</script>

<template>
  <button
    type="button"
    :disabled="!actionable"
    :title="title"
    class="inline-flex items-center gap-1.5 rounded-full border border-current/10 px-2.5 py-1 text-[11px] font-semibold uppercase tracking-wide transition-colors duration-150"
    :class="[config.bg, config.text, actionable ? 'cursor-pointer hover:brightness-110' : 'cursor-default']"
    @click="actionable && emit('act', checkpoint)"
  >
    <component :is="config.icon" class="h-3 w-3" :stroke-width="2" />
    {{ checkpoint.label }}
  </button>
</template>
