<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AIPrimaryAction renders a caller-controlled send/stop state. It does not
  cancel runs or submit messages; those acknowledgements stay provider-owned.
-->
<script setup lang="ts">
import { ArrowUp, Loader2, Square } from 'lucide-vue-next'
import { ensureAgentUIStyles } from '../agentkit/styles'
import type { AIPrimaryActionState } from './ai'

const props = withDefaults(defineProps<{
  state?: AIPrimaryActionState
  type?: 'button' | 'submit' | 'reset'
  disabled?: boolean
  loading?: boolean
  title?: string
  ariaLabel?: string
}>(), {
  state: 'send',
  type: 'button',
  disabled: false,
  loading: false,
  title: '',
  ariaLabel: '',
})

const emit = defineEmits<{
  click: [event: MouseEvent]
}>()

ensureAgentUIStyles()

function defaultLabel(): string {
  if (props.state === 'stopping') return 'Stop requested'
  if (props.state === 'stop') return 'Stop generating'
  return 'Send'
}
</script>

<template>
  <button
    :type="type"
    class="k-ai-primary-action"
    :class="`k-ai-primary-action--${state}`"
    :disabled="disabled || loading"
    :title="title || defaultLabel()"
    :aria-label="ariaLabel || title || defaultLabel()"
    :aria-busy="loading || state === 'stopping' ? 'true' : undefined"
    @click="emit('click', $event)"
  >
    <Loader2 v-if="loading" class="k-ai-primary-action__icon k-spin" :stroke-width="1.75" aria-hidden="true" />
    <Square v-else-if="state !== 'send'" class="k-ai-primary-action__icon k-ai-primary-action__icon--stop" :stroke-width="1.75" fill="currentColor" aria-hidden="true" />
    <ArrowUp v-else class="k-ai-primary-action__icon" :stroke-width="1.75" aria-hidden="true" />
  </button>
</template>
